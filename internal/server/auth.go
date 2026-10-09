package server

import (
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const cookieName = "confhub_admin"

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
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(cookie.Value, claims, func(t *jwt.Token) (any, error) { return []byte(s.options.JWTSecret), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithIssuer("confhub"))
	if err != nil || !token.Valid || claims.Subject != "admin" {
		c.AbortWithStatus(401)
		return
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
	err := s.store.Authenticate(c.Request.Context(), body.Username, body.Password)
	if err != nil {
		slog.Info("admin login", "success", false)
		s.respond(c, nil, err)
		return
	}
	now := time.Now()
	claims := jwt.RegisteredClaims{Subject: "admin", Issuer: "confhub", IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(s.options.JWTExpiry))}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.options.JWTSecret))
	if err != nil {
		s.respond(c, nil, err)
		return
	}
	s.setCookie(c, token, int(s.options.JWTExpiry.Seconds()))
	slog.Info("admin login", "success", true)
	c.JSON(200, gin.H{"username": "admin", "expires_at": claims.ExpiresAt})
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
	err := s.store.ChangePassword(c.Request.Context(), body.Old, body.New)
	if err == nil {
		slog.Info("admin password changed")
	}
	s.respond(c, gin.H{"changed": err == nil}, err)
}
