package oauth

import (
	"context"
	"errors"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Identity is who signed in upstream.
type Identity struct {
	Subject       string
	Email         string
	EmailVerified bool
}

// Upstream signs people in.
type Upstream interface {
	AuthCodeURL(state, verifier string) string
	Exchange(ctx context.Context, code, verifier string) (*Identity, error)
}

// Google signs people in with their Google account.
type Google struct {
	config   *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// NewGoogle discovers Google's endpoints. redirectURL is this server's
// /callback.
func NewGoogle(ctx context.Context, clientID, clientSecret, redirectURL string) (*Google, error) {
	p, err := oidc.NewProvider(ctx, "https://accounts.google.com")
	if err != nil {
		return nil, fmt.Errorf("discover google: %w", err)
	}
	return &Google{
		config:   &oauth2.Config{ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL, Endpoint: p.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "email"}},
		verifier: p.Verifier(&oidc.Config{ClientID: clientID}),
	}, nil
}

// AuthCodeURL is where to send the browser to sign in.
func (g *Google) AuthCodeURL(state, verifier string) string {
	return g.config.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("prompt", "select_account"))
}

// Exchange trades Google's code for the signed-in identity.
func (g *Google) Exchange(ctx context.Context, code, verifier string) (*Identity, error) {
	tok, err := g.config.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("exchange google code: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return nil, errors.New("google sent no id_token")
	}
	idt, err := g.verifier.Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("verify google id_token: %w", err)
	}
	var c struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := idt.Claims(&c); err != nil {
		return nil, fmt.Errorf("read google claims: %w", err)
	}
	return &Identity{Subject: idt.Subject, Email: c.Email, EmailVerified: c.EmailVerified}, nil
}
