package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	uiSessionCookie = "tm_ui_session"
	uiSessionTTL    = 12 * time.Hour
	uiMaxFormSize   = 32 << 10
)

//go:embed ui/*.html ui/*.css ui/*.js
var uiFiles embed.FS

type uiController struct {
	adminToken string
	version    string
	sessionKey [sha256.Size]byte
	dashboard  *template.Template
	login      *template.Template
	now        func() time.Time
	random     io.Reader
}

type uiSessionClaims struct {
	Version   int    `json:"v"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
	CSRF      string `json:"csrf"`
}

type uiPageData struct {
	Version  string
	CSRF     string
	Error    string
	Disabled bool
}

func newUIController(adminToken, version string) (*uiController, error) {
	dashboard, err := template.ParseFS(uiFiles, "ui/dashboard.html")
	if err != nil {
		return nil, fmt.Errorf("прочитать шаблон панели управления: %w", err)
	}
	login, err := template.ParseFS(uiFiles, "ui/login.html")
	if err != nil {
		return nil, fmt.Errorf("прочитать шаблон входа: %w", err)
	}
	return &uiController{
		adminToken: adminToken,
		version:    version,
		sessionKey: sha256.Sum256([]byte("TradingMaster UI session\x00" + adminToken)),
		dashboard:  dashboard,
		login:      login,
		now:        func() time.Time { return time.Now().UTC() },
		random:     rand.Reader,
	}, nil
}

func (ui *uiController) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", ui.root)
	mux.HandleFunc("GET /ui", ui.dashboardPage)
	mux.HandleFunc("GET /ui/login", ui.loginPage)
	mux.HandleFunc("POST /ui/login", ui.loginSubmit)
	mux.HandleFunc("POST /ui/logout", ui.logout)
	mux.HandleFunc("GET /ui/assets/app.css", ui.asset("ui/app.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /ui/assets/app.js", ui.asset("ui/app.js", "text/javascript; charset=utf-8"))
}

func (ui *uiController) root(writer http.ResponseWriter, request *http.Request) {
	http.Redirect(writer, request, "/ui", http.StatusTemporaryRedirect)
}

func (ui *uiController) dashboardPage(writer http.ResponseWriter, request *http.Request) {
	claims, ok := ui.session(request)
	if !ok {
		http.Redirect(writer, request, "/ui/login", http.StatusSeeOther)
		return
	}
	ui.pageHeaders(writer)
	writer.Header().Set("Cache-Control", "no-store")
	if err := ui.dashboard.Execute(writer, uiPageData{Version: ui.version, CSRF: claims.CSRF}); err != nil {
		http.Error(writer, "не удалось сформировать панель управления", http.StatusInternalServerError)
	}
}

func (ui *uiController) loginPage(writer http.ResponseWriter, request *http.Request) {
	if _, ok := ui.session(request); ok {
		http.Redirect(writer, request, "/ui", http.StatusSeeOther)
		return
	}
	ui.renderLogin(writer, http.StatusOK, "")
}

func (ui *uiController) loginSubmit(writer http.ResponseWriter, request *http.Request) {
	if ui.adminToken == "" {
		ui.renderLogin(writer, http.StatusServiceUnavailable, "Вход отключён: администратор должен настроить TM_ADMIN_TOKEN на сервере.")
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, uiMaxFormSize)
	if err := request.ParseForm(); err != nil {
		ui.renderLogin(writer, http.StatusBadRequest, "Форма входа повреждена или слишком велика.")
		return
	}
	if !constantTokenEqual(request.PostFormValue("token"), ui.adminToken) {
		ui.renderLogin(writer, http.StatusUnauthorized, "Неверный административный токен.")
		return
	}
	claims, err := ui.newSession()
	if err != nil {
		ui.renderLogin(writer, http.StatusInternalServerError, "Не удалось создать защищённую сессию.")
		return
	}
	value, err := ui.signSession(claims)
	if err != nil {
		ui.renderLogin(writer, http.StatusInternalServerError, "Не удалось создать защищённую сессию.")
		return
	}
	http.SetCookie(writer, &http.Cookie{
		Name: uiSessionCookie, Value: value, Path: "/", MaxAge: int(uiSessionTTL.Seconds()),
		Expires: time.Unix(claims.ExpiresAt, 0).UTC(), HttpOnly: true,
		Secure: ui.secureRequest(request), SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(writer, request, "/ui", http.StatusSeeOther)
}

func (ui *uiController) logout(writer http.ResponseWriter, request *http.Request) {
	claims, ok := ui.session(request)
	if !ok {
		ui.unauthorized(writer)
		return
	}
	if !constantTokenEqual(request.Header.Get("X-CSRF-Token"), claims.CSRF) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "CSRF-проверка не пройдена"})
		return
	}
	http.SetCookie(writer, &http.Cookie{
		Name: uiSessionCookie, Value: "", Path: "/", MaxAge: -1,
		Expires: time.Unix(1, 0).UTC(), HttpOnly: true,
		Secure: ui.secureRequest(request), SameSite: http.SameSiteStrictMode,
	})
	writeJSON(writer, http.StatusOK, map[string]bool{"logged_out": true})
}

func (ui *uiController) authorizeAPI(writer http.ResponseWriter, request *http.Request) bool {
	if ui.adminToken == "" {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{
			"error": "административный API отключён: TM_ADMIN_TOKEN не настроен",
		})
		return false
	}
	if token, found := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer "); found &&
		constantTokenEqual(token, ui.adminToken) {
		return true
	}
	claims, ok := ui.session(request)
	if !ok {
		ui.unauthorized(writer)
		return false
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead && request.Method != http.MethodOptions &&
		!constantTokenEqual(request.Header.Get("X-CSRF-Token"), claims.CSRF) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "CSRF-проверка не пройдена"})
		return false
	}
	return true
}

func (ui *uiController) unauthorized(writer http.ResponseWriter) {
	writer.Header().Set("WWW-Authenticate", "Bearer")
	writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "требуется корректный admin token или UI-сессия"})
}

func (ui *uiController) newSession() (uiSessionClaims, error) {
	csrf := make([]byte, 32)
	if _, err := io.ReadFull(ui.random, csrf); err != nil {
		return uiSessionClaims{}, err
	}
	now := ui.now()
	return uiSessionClaims{
		Version: 1, IssuedAt: now.Unix(), ExpiresAt: now.Add(uiSessionTTL).Unix(),
		CSRF: base64.RawURLEncoding.EncodeToString(csrf),
	}, nil
}

func (ui *uiController) signSession(claims uiSessionClaims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, ui.sessionKey[:])
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (ui *uiController) session(request *http.Request) (uiSessionClaims, bool) {
	cookie, err := request.Cookie(uiSessionCookie)
	if err != nil || ui.adminToken == "" {
		return uiSessionClaims{}, false
	}
	encoded, signature, found := strings.Cut(cookie.Value, ".")
	if !found || encoded == "" || signature == "" {
		return uiSessionClaims{}, false
	}
	provided, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return uiSessionClaims{}, false
	}
	mac := hmac.New(sha256.New, ui.sessionKey[:])
	_, _ = mac.Write([]byte(encoded))
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return uiSessionClaims{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return uiSessionClaims{}, false
	}
	var claims uiSessionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return uiSessionClaims{}, false
	}
	now := ui.now().Unix()
	if claims.Version != 1 || claims.ExpiresAt <= now || claims.IssuedAt > now+60 || claims.CSRF == "" {
		return uiSessionClaims{}, false
	}
	return claims, true
}

func (ui *uiController) renderLogin(writer http.ResponseWriter, status int, message string) {
	ui.pageHeaders(writer)
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	if err := ui.login.Execute(writer, uiPageData{
		Version: ui.version, Error: message, Disabled: ui.adminToken == "",
	}); err != nil {
		_, _ = fmt.Fprint(writer, "не удалось сформировать страницу входа")
	}
}

func (ui *uiController) asset(name, contentType string) http.HandlerFunc {
	content, err := uiFiles.ReadFile(name)
	return func(writer http.ResponseWriter, _ *http.Request) {
		ui.pageHeaders(writer)
		writer.Header().Set("Content-Type", contentType)
		writer.Header().Set("Cache-Control", "public, max-age=300")
		if err != nil {
			http.Error(writer, "ресурс интерфейса не найден", http.StatusInternalServerError)
			return
		}
		_, _ = writer.Write(content)
	}
}

func (ui *uiController) pageHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Frame-Options", "DENY")
	writer.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
}

func (ui *uiController) secureRequest(request *http.Request) bool {
	if request.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(request.Header.Get("X-Forwarded-Proto")), "https")
}

func constantTokenEqual(provided, expected string) bool {
	providedHash := sha256.Sum256([]byte(provided))
	expectedHash := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) == 1
}
