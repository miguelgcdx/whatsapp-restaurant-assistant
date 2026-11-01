package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"restaurant-assistant/backend/internal/staff"
	"restaurant-assistant/backend/internal/stafforders"
)

const bffTransportFrontend = "/home/miguelgcd/Projects/ai/rags/rag1/whatsapp-restaurant-assistant/restaurant-assistant/.worktrees/frontend"
const bffTransportNode = "/home/miguelgcd/.local/share/mise/installs/node/26.11.1/bin/node"

// TestOrdersBFFTransport is a separate opt-in transport observation, not closure
// of any previous independent proof. The frontend session is synthetic; HTTP,
// JWT verification, current membership, orders and disposable PostgreSQL are real.
// Production orders handler/dispatcher use a BRIDGE-COMPOSED real verifier, not
// unmodified host-constructor auth config or a real frontend issuer/session.
// Parent orchestration must pin/release the frontend candidate before opting in.
func TestOrdersBFFTransport(t *testing.T) {
	root := os.Getenv("STAFF_ORDERS_BFF_FRONTEND_ROOT")
	if root == "" {
		t.Skip("STAFF_ORDERS_BFF_FRONTEND_ROOT is unset; BFF transport was not executed")
	}
	if testing.Short() {
		t.Skip("BFF transport requires installed Node and isolated PostgreSQL binaries")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != bffTransportFrontend {
		t.Fatal("STAFF_ORDERS_BFF_FRONTEND_ROOT must select the canonical absolute frontend worktree")
	}
	info, err := os.Stat(root)
	connectedCheck(t, err)
	if !info.IsDir() {
		t.Fatal("frontend root is not an existing directory")
	}
	for _, path := range []string{
		bffTransportNode,
		filepath.Join(root, "node_modules/vitest/vitest.mjs"),
		filepath.Join(root, "vitest.pickup-connected.config.mts"),
		filepath.Join(root, "tests/integration/staff-orders-bff.connected.mts"),
	} {
		info, err := os.Stat(path)
		connectedCheck(t, err)
		if !info.Mode().IsRegular() || (path == bffTransportNode && info.Mode().Perm()&0111 == 0) {
			t.Fatalf("required installed transport input is not a regular file/executable: %s", path)
		}
	}
	// Never let MkdirTemp silently fall back to another temporary location.
	tmp := os.Getenv("TMPDIR")
	if !filepath.IsAbs(tmp) || os.Getenv("TMP") != tmp || os.Getenv("TEMP") != tmp {
		t.Fatal("explicit matching absolute TMPDIR/TMP/TEMP are required")
	}
	info, err = os.Lstat(tmp)
	connectedCheck(t, err)
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("supplied temporary root must be a private 0700 directory, not a symlink")
	}
	evidence, err := os.MkdirTemp(tmp, "bff-transport-")
	connectedCheck(t, err)
	// Deliberately retained outside t.TempDir: only synthetic fixture credentials
	// and child receipts live here, never provider credentials or environment dumps.
	t.Logf("private BFF transport evidence retained at %s", evidence)
	writePrivate := func(name string, data []byte) {
		t.Helper()
		connectedCheck(t, os.WriteFile(filepath.Join(evidence, name), data, 0600))
	}

	f := connectedSetup(t)
	// The original fixture's signer is private to connectedSetup. Compose a new
	// real verifier for orders only, retaining its seeded store and current members.
	public, private, err := ed25519.GenerateKey(rand.Reader)
	connectedCheck(t, err)
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/auth/jwks" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"keys":[{"kty":"OKP","crv":"Ed25519","alg":"EdDSA","kid":"bff-transport","x":%q}]}`, base64.RawURLEncoding.EncodeToString(public))
	}))
	t.Cleanup(keys.Close)
	verifier, err := staff.NewVerifier(keys.URL, keys.URL+"/api/auth/jwks")
	connectedCheck(t, err)
	orders, err := stafforders.NewHandler(f.pickup, verifier)
	connectedCheck(t, err)
	f.handler = dispatch(http.NotFoundHandler(), http.NotFoundHandler(), http.NotFoundHandler(), orders)
	server := httptest.NewServer(f.handler)
	t.Cleanup(server.Close)
	origin, err := staff.LocalOrigin(server.URL)
	connectedCheck(t, err)
	if origin.Scheme != "http" || origin.Hostname() != "127.0.0.1" {
		t.Fatal("owned backend server must use numeric IPv4 loopback HTTP")
	}

	fixturePath := filepath.Join(evidence, "fixture.json")
	args := []string{
		filepath.Join(root, "node_modules/vitest/vitest.mjs"),
		"run", "--config", "vitest.pickup-connected.config.mts",
		"tests/integration/staff-orders-bff.connected.mts",
	}
	command, err := json.Marshal(struct {
		Executable string   `json:"executable"`
		Args       []string `json:"args"`
		Directory  string   `json:"directory"`
	}{bffTransportNode, args, root})
	connectedCheck(t, err)
	writePrivate("child.command", command)
	stdout, err := os.OpenFile(filepath.Join(evidence, "child.stdout"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	connectedCheck(t, err)
	defer stdout.Close()
	stderr, err := os.OpenFile(filepath.Join(evidence, "child.stderr"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	connectedCheck(t, err)
	defer stderr.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bffTransportNode, args...)
	cmd.Dir = root
	// Positive allowlist: no ambient DB/auth/provider credentials, NODE_OPTIONS
	// hooks or environment-file options. HOME is passed unchanged and never read.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"),
		"TMPDIR=" + evidence, "TMP=" + evidence, "TEMP=" + evidence,
		"NODE_OPTIONS=", "STAFF_ORDERS_BFF_FIXTURE=" + fixturePath,
		"CI=1", "NO_COLOR=1", "NODE_ENV=test",
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr

	// Mint immediately before fixture publication and child launch, never renew.
	now := time.Now()
	claims := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{
		"sub": "connected-operator", "iss": keys.URL,
		"aud": "restaurant-assistant-api", "iat": now.Unix(),
		"exp": now.Add(59 * time.Second).Unix(),
	})
	claims.Header["kid"] = "bff-transport"
	token, err := claims.SignedString(private)
	connectedCheck(t, err)
	fixture, err := json.Marshal(struct {
		GoOrigin       string `json:"go_origin"`
		WebOrigin      string `json:"web_origin"`
		Token          string `json:"token"`
		OrderID        string `json:"order_id"`
		SummaryID      string `json:"summary_id"`
		InitialVersion string `json:"initial_version"`
	}{server.URL, "http://localhost:3000", token, f.order.ID.String(), f.order.Confirmed.ID.String(), strconv.FormatInt(f.order.Version.Int64(), 10)})
	connectedCheck(t, err)
	writePrivate("fixture.json", fixture)
	runErr := cmd.Run() // Synchronous Wait finishes before any fixture cleanup.
	stdoutErr, stderrErr := stdout.Close(), stderr.Close()
	exitCode := -1 // No process exit status exists if Start failed.
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode() // -1 also identifies signal termination.
	}
	result := fmt.Sprintf("exit_code=%d\ntimeout=%t\n", exitCode, ctx.Err() == context.DeadlineExceeded)
	if runErr != nil {
		result += "error=" + strings.ReplaceAll(runErr.Error(), token, "[synthetic JWT redacted]") + "\n"
	}
	writePrivate("child.exit", []byte(result))
	// Redact before truncation so even a token crossing the output boundary is safe.
	for _, name := range []string{"child.stdout", "child.stderr"} {
		file, err := os.Open(filepath.Join(evidence, name))
		connectedCheck(t, err)
		data, readErr := io.ReadAll(io.LimitReader(file, int64(8192+len(token))))
		closeErr := file.Close()
		connectedCheck(t, readErr)
		connectedCheck(t, closeErr)
		text := string(data)
		// A bounded read can end inside a JWT. Remove any trailing prefix too,
		// before full-token replacement can shorten the output below its cap.
		for n := len(token) - 1; n > 0; n-- {
			if strings.HasSuffix(text, token[:n]) {
				text = text[:len(text)-n] + "[synthetic JWT redacted]"
				break
			}
		}
		text = strings.ReplaceAll(text, token, "[synthetic JWT redacted]")
		if len(text) > 8192 {
			text = text[:8192] + "\n[bounded output; full raw receipt retained privately]"
		}
		t.Logf("%s (redacted):\n%s", name, text)
	}
	t.Logf("BFF transport child result: %s", strings.TrimSpace(result))
	connectedCheck(t, stdoutErr)
	connectedCheck(t, stderrErr)
	if runErr != nil || ctx.Err() != nil || exitCode != 0 {
		t.Fatalf("BFF transport child failed; exit=%d context=%v; retained evidence: %s (no retry)", exitCode, ctx.Err(), evidence)
	}
	t.Log("separate BFF transport observation only; not real frontend-session or independent-review acceptance")
}
