package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/vincent-vinf/confhub/internal/config"
)

const cookieName = "confhub_admin"

type userClaims struct {
	jwt.RegisteredClaims
	AuthVersion int64 `json:"auth_version"`
}

func (s *Server) sameOrigin(c *gin.Context) {
	if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
		c.Next()
		return
	}
	raw := c.GetHeader("Origin")
	if raw == "" {
		raw = c.GetHeader("Referer")
	}
	u, err := url.Parse(raw)
	scheme := "http"
	if c.Request.TLS != nil || s.options.CookieSecure {
		scheme = "https"
	}
	if err != nil || u.Host != c.Request.Host || u.Scheme != scheme || u.User != nil || c.GetHeader("Sec-Fetch-Site") == "cross-site" {
		c.AbortWithStatusJSON(403, gin.H{"error": "same-origin request required"})
		return
	}
	c.Next()
}
func (s *Server) authenticate(c *gin.Context) {
	cookie, err := c.Request.Cookie(cookieName)
	if err != nil {
		c.AbortWithStatus(401)
		return
	}
	claims := &userClaims{}
	token, err := jwt.ParseWithClaims(cookie.Value, claims, func(t *jwt.Token) (any, error) { return []byte(s.options.JWTSecret), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithIssuer("confhub"))
	if err != nil || !token.Valid || claims.Subject == "" || claims.AuthVersion < 1 {
		c.AbortWithStatus(401)
		return
	}
	user, err := s.store.User(c.Request.Context(), claims.Subject)
	if err == nil {
		auditUser(c, user)
	}
	if errors.Is(err, config.ErrNotFound) || err == nil && (!user.Enabled || user.AuthVersion != claims.AuthVersion) {
		c.AbortWithStatus(401)
		return
	}
	if err != nil {
		s.respond(c, nil, err)
		return
	}
	c.Set("user", user)
	if a := auditRecord(c); a != nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		err := s.store.IdentifyAudit(ctx, a.ID, user)
		cancel()
		if err != nil {
			c.AbortWithStatusJSON(503, gin.H{"error": "audit storage unavailable"})
			return
		}
	}
	c.Next()
}
func (s *Server) setCookie(c *gin.Context, value string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{Name: cookieName, Value: value, Path: "/api/admin", HttpOnly: true, Secure: s.options.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}
func (s *Server) login(c *gin.Context) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !bind(c, &body) {
		return
	}
	if a := auditRecord(c); a != nil {
		a.AttemptedUsername = auditText(body.Username, 64)
	}
	user, err := s.store.Authenticate(c.Request.Context(), body.Username, body.Password)
	if err != nil {
		slog.Info("user login", "success", false)
		s.respond(c, nil, err)
		return
	}
	auditUser(c, user)
	now := time.Now()
	claims := userClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: user.ID, Issuer: "confhub", IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(s.options.JWTExpiry))}, AuthVersion: user.AuthVersion}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.options.JWTSecret))
	if err != nil {
		s.respond(c, nil, err)
		return
	}
	s.setCookie(c, token, int(s.options.JWTExpiry.Seconds()))
	slog.Info("user login", "success", true, "user_id", user.ID)
	c.JSON(200, gin.H{"username": user.Username, "expires_at": claims.ExpiresAt})
}
func (s *Server) logout(c *gin.Context) { s.setCookie(c, "", -1); c.Status(204) }
func (s *Server) password(c *gin.Context) {
	var body struct {
		Old string `json:"old_password"`
		New string `json:"new_password"`
	}
	if !bind(c, &body) {
		return
	}
	user := currentUser(c)
	err := s.store.ChangePassword(c.Request.Context(), user.ID, user.AuthVersion, body.Old, body.New)
	if err == nil {
		s.setCookie(c, "", -1)
		slog.Info("password changed", "user_id", user.ID)
	}
	s.respond(c, gin.H{"changed": err == nil}, err)
}
