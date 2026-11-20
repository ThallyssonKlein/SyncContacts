package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Extra scope used only to discover the signed-in account's email, so the user
// can verify which account is the source and which is the destination.
const userinfoScope = "https://www.googleapis.com/auth/userinfo.email"

type account struct {
	Email  string
	Client *http.Client
}

// authenticate returns an authenticated HTTP client for an account. It uses the
// token saved at tokenPath when it still works; otherwise it opens the browser
// for the user to sign in and saves the new token.
func authenticate(ctx context.Context, credsPath, tokenPath, role, scope string) (*account, error) {
	b, err := os.ReadFile(credsPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s (see the README to generate this file): %w", credsPath, err)
	}
	cfg, err := google.ConfigFromJSON(b, scope, userinfoScope)
	if err != nil {
		return nil, fmt.Errorf("invalid credentials in %s: %w", credsPath, err)
	}

	if tok, err := loadToken(tokenPath); err == nil {
		client := cfg.Client(ctx, tok)
		if email, err := fetchEmail(ctx, client); err == nil {
			return &account{Email: email, Client: client}, nil
		}
		fmt.Printf("The saved token for the %s account no longer works; please sign in again.\n", role)
	}

	tok, err := loginInteractive(ctx, cfg, role)
	if err != nil {
		return nil, err
	}
	if err := saveToken(tokenPath, tok); err != nil {
		return nil, err
	}
	client := cfg.Client(ctx, tok)
	email, err := fetchEmail(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("getting the email of the %s account: %w", role, err)
	}
	return &account{Email: email, Client: client}, nil
}

// loginInteractive runs the desktop-app OAuth flow: starts a local server on
// 127.0.0.1, opens the browser and waits for Google to redirect with the code.
func loginInteractive(ctx context.Context, base *oauth2.Config, role string) (*oauth2.Token, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("opening a local port for the login: %w", err)
	}
	defer ln.Close()

	cfg := *base
	cfg.RedirectURL = "http://" + ln.Addr().String()
	state := randomHex(16)
	verifier := oauth2.GenerateVerifier()
	authURL := cfg.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.S256ChallengeOption(verifier),
		// Forces the account chooser; otherwise Google signs in straight to
		// the browser's active account and both accounts end up being the same.
		oauth2.SetAuthURLParam("prompt", "select_account consent"),
	)

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	send := func(r result) {
		select {
		case done <- r:
		default:
		}
	}
	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("state") != state {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if e := q.Get("error"); e != "" {
				fmt.Fprint(w, "<p>Authorization denied. You can close this tab.</p>")
				send(result{err: fmt.Errorf("authorization denied by Google: %s", e)})
				return
			}
			fmt.Fprintf(w, "<p>%s account authorized. You can close this tab and go back to the terminal.</p>", role)
			send(result{code: q.Get("code")})
		}),
	}
	go srv.Serve(ln)
	defer srv.Shutdown(context.Background())

	fmt.Printf("\n>>> Sign in with the %s account.\n", role)
	fmt.Printf("If the browser does not open by itself, go to:\n%s\n\n", authURL)
	openBrowser(authURL)

	select {
	case r := <-done:
		if r.err != nil {
			return nil, r.err
		}
		tok, err := cfg.Exchange(ctx, r.code, oauth2.VerifierOption(verifier))
		if err != nil {
			return nil, fmt.Errorf("exchanging the code for a token: %w", err)
		}
		return tok, nil
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("timed out waiting for the %s account login", role)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func fetchEmail(ctx context.Context, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openidconnect.googleapis.com/v1/userinfo", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("userinfo responded %s: %s", resp.Status, body)
	}
	var info struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", err
	}
	if info.Email == "" {
		return "", fmt.Errorf("userinfo returned no email")
	}
	return info.Email, nil
}

func loadToken(path string) (*oauth2.Token, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal(b, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

func saveToken(path string, tok *oauth2.Token) error {
	b, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("saving token to %s: %w", path, err)
	}
	return nil
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
