package auth

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/config"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const cookieName = "bp_session"

//go:embed schema.sql
var authSchemaSQL string

type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

type Claims struct {
	UserID int64  `json:"uid"`
	Login  string `json:"login"`
	jwt.RegisteredClaims
}

type Manager struct {
	db        *sql.DB
	jwtSecret []byte
	tokenTTL  time.Duration
}

func New(ctx context.Context, cfg config.Config) (*Manager, error) {
	if strings.TrimSpace(cfg.AuthDBDSN) == "" {
		return nil, errors.New("AUTH_DB_DSN is empty")
	}
	if strings.TrimSpace(cfg.AuthJWTSecret) == "" {
		return nil, errors.New("AUTH_JWT_SECRET is empty")
	}

	db, err := sql.Open("pgx", cfg.AuthDBDSN)
	if err != nil {
		return nil, fmt.Errorf("open auth db: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping auth db: %w", err)
	}

	m := &Manager{
		db:        db,
		jwtSecret: []byte(cfg.AuthJWTSecret),
		tokenTTL:  time.Duration(cfg.AuthTokenTTLMinutes) * time.Minute,
	}
	if m.tokenTTL <= 0 {
		m.tokenTTL = 12 * time.Hour
	}

	if err := m.initSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	seedUsers := []struct {
		Login    string
		Password string
	}{
		{Login: strings.TrimSpace(cfg.AuthUser1Login), Password: cfg.AuthUser1Password},
		{Login: strings.TrimSpace(cfg.AuthUser2Login), Password: cfg.AuthUser2Password},
	}
	for _, u := range seedUsers {
		if u.Login == "" || strings.TrimSpace(u.Password) == "" {
			continue
		}
		if err := m.upsertUser(ctx, u.Login, u.Password); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("seed auth user %q: %w", u.Login, err)
		}
	}

	return m, nil
}

func (m *Manager) Close() error {
	if m == nil || m.db == nil {
		return nil
	}
	return m.db.Close()
}

func (m *Manager) initSchema(ctx context.Context) error {
	_, err := m.db.ExecContext(ctx, authSchemaSQL)
	if err != nil {
		return fmt.Errorf("create auth_users table: %w", err)
	}
	return nil
}

func (m *Manager) upsertUser(ctx context.Context, login, password string) error {
	hashBytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	const q = `
INSERT INTO auth_users (login, password_hash)
VALUES ($1, $2)
ON CONFLICT (login)
DO UPDATE SET password_hash = EXCLUDED.password_hash, updated_at = NOW();`
	_, err = m.db.ExecContext(ctx, q, strings.TrimSpace(login), string(hashBytes))
	if err != nil {
		return fmt.Errorf("upsert auth user: %w", err)
	}
	return nil
}

func (m *Manager) Authenticate(ctx context.Context, login, password string) (*User, error) {
	const q = `SELECT id, login, password_hash FROM auth_users WHERE login = $1 LIMIT 1;`
	var id int64
	var dbLogin, hash string
	err := m.db.QueryRowContext(ctx, q, strings.TrimSpace(login)).Scan(&id, &dbLogin, &hash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("invalid credentials")
		}
		return nil, fmt.Errorf("load user: %w", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, errors.New("invalid credentials")
	}
	return &User{ID: id, Login: dbLogin}, nil
}

func (m *Manager) IssueToken(u *User) (string, time.Time, error) {
	if u == nil {
		return "", time.Time{}, errors.New("nil user")
	}
	now := time.Now().UTC()
	expiresAt := now.Add(m.tokenTTL)
	claims := Claims{
		UserID: u.ID,
		Login:  u.Login,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(u.ID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token, err := t.SignedString(m.jwtSecret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign jwt: %w", err)
	}
	return token, expiresAt, nil
}

func (m *Manager) ParseToken(token string) (*Claims, error) {
	t, err := jwt.ParseWithClaims(token, &Claims{}, func(tok *jwt.Token) (any, error) {
		if tok.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method: %s", tok.Method.Alg())
		}
		return m.jwtSecret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := t.Claims.(*Claims)
	if !ok || !t.Valid {
		return nil, errors.New("invalid token")
	}
	if claims.UserID <= 0 || strings.TrimSpace(claims.Login) == "" {
		return nil, errors.New("invalid token claims")
	}
	return claims, nil
}

func CookieName() string {
	return cookieName
}
