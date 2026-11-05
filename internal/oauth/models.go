package oauth

import (
	"time"
)

// Client is an MCP client that registered itself (RFC 7591).
type Client struct {
	ID           string   `gorm:"primaryKey"`
	SecretHash   *string  `gorm:""`
	Name         string   `gorm:"not null"`
	RedirectURIs []string `gorm:"serializer:json;not null"`
	AuthMethod   string   `gorm:"not null"`
	CreatedAt    time.Time
}

// TableName keeps OAuth tables apart from food data.
func (Client) TableName() string { return "oauth_clients" }

// AuthRequest is an /authorize call waiting for Google sign-in.
type AuthRequest struct {
	ID               string `gorm:"primaryKey"`
	ClientID         string `gorm:"not null"`
	RedirectURI      string `gorm:"not null"`
	State            string
	CodeChallenge    string    `gorm:"not null"`
	UpstreamVerifier string    `gorm:"not null"`
	ExpiresAt        time.Time `gorm:"not null;index"`
}

// TableName keeps OAuth tables apart from food data.
func (AuthRequest) TableName() string { return "oauth_auth_requests" }

// AuthCode is a one-time code for /token. Only its hash is stored.
type AuthCode struct {
	Hash          string    `gorm:"primaryKey"`
	ClientID      string    `gorm:"not null"`
	UserID        string    `gorm:"not null"`
	RedirectURI   string    `gorm:"not null"`
	CodeChallenge string    `gorm:"not null"`
	ExpiresAt     time.Time `gorm:"not null;index"`
}

// TableName keeps OAuth tables apart from food data.
func (AuthCode) TableName() string { return "oauth_codes" }

// Token is an access or refresh token. Only its hash is stored. Tokens from
// one sign-in share a GrantID, so revoking the refresh token ends them all.
type Token struct {
	Hash      string    `gorm:"primaryKey"`
	Kind      string    `gorm:"not null"`
	ClientID  string    `gorm:"not null"`
	UserID    string    `gorm:"not null;index"`
	GrantID   string    `gorm:"not null;index"`
	ExpiresAt time.Time `gorm:"not null;index"`
	CreatedAt time.Time
}

// TableName keeps OAuth tables apart from food data.
func (Token) TableName() string { return "oauth_tokens" }

const (
	kindAccess  = "access"
	kindRefresh = "refresh"
)
