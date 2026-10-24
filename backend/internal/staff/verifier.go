package staff

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// LocalOrigin accepts only an explicit numeric-loopback HTTP(S) origin.
func LocalOrigin(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u == nil {
		return nil, ErrInput
	}
	ip := net.ParseIP(u.Hostname())
	port, e := strconv.Atoi(u.Port())
	if (u.Scheme != "http" && u.Scheme != "https") || ip == nil || !ip.IsLoopback() || e != nil || port < 1 || port > 65535 || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" || u.Opaque != "" {
		return nil, ErrInput
	}
	return u, nil
}

type Verifier struct {
	issuer, source     string
	client             *http.Client
	mu                 sync.Mutex
	keys               map[string]ed25519.PublicKey
	expires, nextFetch time.Time
}

// NewVerifier fixes the trusted source; token headers can never select a URL.
// Refresh is request-driven, at most once per five seconds, with one minute TTL.
func NewVerifier(issuer, source string) (*Verifier, error) {
	if _, e := LocalOrigin(issuer); e != nil {
		return nil, ErrInput
	}
	u, e := url.Parse(source)
	if e != nil || u == nil || u.Path != "/api/auth/jwks" || u.RawPath != "" {
		return nil, ErrInput
	}
	origin := *u
	origin.Path = ""
	if _, e = LocalOrigin(origin.String()); e != nil {
		return nil, ErrInput
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, MaxConnsPerHost: 2, MaxIdleConnsPerHost: 2, IdleConnTimeout: time.Minute, TLSHandshakeTimeout: time.Second, ResponseHeaderTimeout: time.Second}
	return &Verifier{issuer: issuer, source: source, client: &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

type accessClaims struct{ jwt.RegisteredClaims }

// jwt's default NumericDate decoder truncates fractions to the global
// TimePrecision. Preserve them per token without mutating shared parser state.
func (c *accessClaims) UnmarshalJSON(data []byte) error {
	var wire struct {
		jwt.RegisteredClaims
		IssuedAt  json.RawMessage `json:"iat"`
		ExpiresAt json.RawMessage `json:"exp"`
		NotBefore json.RawMessage `json:"nbf"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return ErrUnauthenticated
	}
	c.RegisteredClaims = wire.RegisteredClaims
	for _, field := range []struct {
		raw    json.RawMessage
		target **jwt.NumericDate
	}{
		{wire.IssuedAt, &c.IssuedAt},
		{wire.ExpiresAt, &c.ExpiresAt},
		{wire.NotBefore, &c.NotBefore},
	} {
		if len(field.raw) == 0 || string(field.raw) == "null" {
			continue
		}
		var seconds float64
		if err := json.Unmarshal(field.raw, &seconds); err != nil || math.IsInf(seconds, 0) || math.IsNaN(seconds) {
			return ErrUnauthenticated
		}
		whole, fraction := math.Modf(seconds)
		if whole <= float64(math.MinInt64) || whole >= float64(math.MaxInt64) {
			return ErrUnauthenticated
		}
		*field.target = &jwt.NumericDate{Time: time.Unix(int64(whole), int64(fraction*float64(time.Second)))}
	}
	return nil
}

func (c accessClaims) Validate() error {
	if !validSubject(c.Subject) || c.IssuedAt == nil || c.ExpiresAt == nil || len(c.Audience) != 1 || c.Audience[0] != "restaurant-assistant-api" {
		return ErrUnauthenticated
	}
	life := c.ExpiresAt.Time.Sub(c.IssuedAt.Time)
	if life <= 0 || life > time.Minute || c.IssuedAt.Time.After(time.Now()) {
		return ErrUnauthenticated
	}
	return nil
}

// Verify returns only the authenticated subject; all parser errors are redacted.
func (v *Verifier) Verify(ctx context.Context, raw string) (string, error) {
	if len(raw) == 0 || len(raw) > 8192 {
		return "", ErrUnauthenticated
	}
	c := &accessClaims{}
	var dependency bool
	parsed, err := jwt.ParseWithClaims(raw, c, func(t *jwt.Token) (any, error) {
		kid, ok := t.Header["kid"].(string)
		if !ok || len(kid) == 0 || len(kid) > 128 {
			return nil, ErrUnauthenticated
		}
		key, e := v.key(ctx, kid)
		dependency = errors.Is(e, ErrUnavailable)
		return key, e
	}, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer(v.issuer), jwt.WithAudience("restaurant-assistant-api"), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithStrictDecoding())
	if dependency {
		return "", ErrUnavailable
	}
	if err != nil || parsed == nil || !parsed.Valid {
		return "", ErrUnauthenticated
	}
	return c.Subject, nil
}
func (v *Verifier) key(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := time.Now()
	if key, ok := v.keys[kid]; ok && now.Before(v.expires) {
		return key, nil
	}
	if now.Before(v.nextFetch) {
		if !now.Before(v.expires) {
			return nil, ErrUnavailable
		}
		return nil, ErrUnauthenticated
	}
	v.nextFetch = now.Add(5 * time.Second)
	keys, e := v.fetch(ctx)
	if e != nil {
		return nil, ErrUnavailable
	}
	v.keys = keys
	v.expires = time.Now().Add(time.Minute)
	if key, ok := keys[kid]; ok {
		return key, nil
	}
	return nil, ErrUnauthenticated
}
func (v *Verifier) fetch(ctx context.Context) (map[string]ed25519.PublicKey, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, v.source, nil)
	if e != nil {
		return nil, ErrUnavailable
	}
	res, e := v.client.Do(req)
	if e != nil {
		return nil, ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, ErrUnavailable
	}
	body, e := io.ReadAll(io.LimitReader(res.Body, 65537))
	if e != nil || len(body) > 65536 {
		return nil, ErrUnavailable
	}
	var set struct {
		Keys []struct {
			Kty string          `json:"kty"`
			Crv string          `json:"crv"`
			Alg string          `json:"alg"`
			Use string          `json:"use"`
			Kid string          `json:"kid"`
			X   string          `json:"x"`
			D   json.RawMessage `json:"d"`
		} `json:"keys"`
	}
	if e = json.Unmarshal(body, &set); e != nil || len(set.Keys) == 0 || len(set.Keys) > 32 {
		return nil, ErrUnavailable
	}
	keys := make(map[string]ed25519.PublicKey, len(set.Keys))
	for _, j := range set.Keys {
		if j.Kty != "OKP" || j.Crv != "Ed25519" || (j.Alg != "" && j.Alg != "EdDSA") || (j.Use != "" && j.Use != "sig") || len(j.Kid) == 0 || len(j.Kid) > 128 || len(j.D) != 0 {
			return nil, ErrUnavailable
		}
		x, e := base64.RawURLEncoding.Strict().DecodeString(j.X)
		if e != nil || len(x) != ed25519.PublicKeySize {
			return nil, ErrUnavailable
		}
		if _, duplicate := keys[j.Kid]; duplicate {
			return nil, ErrUnavailable
		}
		keys[j.Kid] = ed25519.PublicKey(x)
	}
	return keys, nil
}
