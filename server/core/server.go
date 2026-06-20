package core

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"log"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/render"
	"github.com/google/uuid"
)

// Global authenticated state
var authenticated atomic.Bool
var authUpdateTime atomic.Int64

var authSignal = make(chan struct{})
var authSignalMu sync.Mutex

type QRCode struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	Host       string `json:"host"`
	UpdateTime int64  `json:"update_time,omitifempty"`
	Authorized bool   `json:"authorized"`
	signal     chan struct{}
}

type HttpServer struct {
	r       *chi.Mux
	QRCodes sync.Map
}

func NewHttpServer() (*HttpServer, error) {
	o := &HttpServer{
		r:       chi.NewRouter(),
		QRCodes: sync.Map{},
	}

	authUpdateTime.Store(time.Now().UnixMilli())

	return o, nil
}

func signalAuthenticationChanged() {
	authSignalMu.Lock()
	defer authSignalMu.Unlock()

	close(authSignal)
	authSignal = make(chan struct{})
}

func (o *HttpServer) Run(wwwdir string) {
	o.r.Use(middleware.Logger)

	workDir, _ := os.Getwd()
	filesDir := http.Dir(filepath.Join(workDir, wwwdir))

	o.r.Route("/qrcode", func(r chi.Router) {
		r.Route("/{id}", func(r chi.Router) {
			r.Use(o.qrcodeCtx)
			r.With(Authenticator(API_TOKEN)).Put("/", o.PutQRCode)
			r.Get("/", o.GetQRCode)
		})
	})

	o.r.Get("/qrreset", o.QRReset)
	
	// AUTHENTICATED ROUTES
	o.r.Route("/authenticated", func(r chi.Router) {
		r.With(Authenticator(API_TOKEN)).Put("/", o.PutAuthenticated)
		r.Get("/", o.GetAuthenticated)
	})

	o.FileServer(o.r, "/*", filesDir)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := BIND_ADDRESS + port
	log.Fatal(http.ListenAndServe(addr, o.r))

}

func (o *HttpServer) qrcodeCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var qrcode *QRCode = nil

		id := strings.ToLower(chi.URLParam(r, "id"))
		_, err := uuid.Parse(id)
		if err == nil {
			qrcode = o._getQRCode(id)
		} else {
			http.Error(w, "", http.StatusNotFound)
			return
		}

		ctx := context.WithValue(r.Context(), "qrcode", qrcode)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (o *HttpServer) PutQRCode(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(chi.URLParam(r, "id"))

	var data QRCode
	err := json.NewDecoder(r.Body).Decode(&data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var doSignal bool = false
	qrcode := o._getQRCode(id)

	if qrcode == nil {
		qrcode = &data
		qrcode.signal = make(chan struct{})
	} else {
		doSignal = true
	}

	qrcode.Source = data.Source
	qrcode.Host = data.Host
	qrcode.UpdateTime = time.Now().UnixMilli()

	o.QRCodes.Store(id, qrcode)

	if doSignal {
		if qrcode.signal != nil {
			close(qrcode.signal)
			qrcode.signal = make(chan struct{})
		}
	}

	// QR update invalidates authentication
	if authenticated.Load() {
		authenticated.Store(false)
		authUpdateTime.Store(time.Now().UnixMilli())
		signalAuthenticationChanged()
	}

	render.Render(w, r, NewQRCodeResponse(qrcode))
}

func (o *HttpServer) GetQRCode(w http.ResponseWriter, r *http.Request) {
	var err error
	var fromTime int64

	id := strings.ToLower(chi.URLParam(r, "id"))
	_fromTime := r.URL.Query().Get("t")
	if _fromTime != "" {
		fromTime, err = strconv.ParseInt(_fromTime, 10, 64)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	qrcode := o._getQRCode(id)
	if qrcode == nil {
		http.Error(w, "", http.StatusNotFound)
		return
	}

	if fromTime > 0 {
		if fromTime >= qrcode.UpdateTime {
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()

			select {
			case <-ticker.C:
				http.Error(w, "", http.StatusRequestTimeout)
				return

			case <-qrcode.signal:
			}

			qrcode = o._getQRCode(id)
		}
	}

	render.Render(w, r, NewQRCodeResponse(qrcode))
}

// -----------------------------------------------------
// AUTHENTICATED HANDLERS
// -----------------------------------------------------

func (o *HttpServer) PutAuthenticated(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Authenticated bool `json:"authenticated"`
	}

	err := json.NewDecoder(r.Body).Decode(&payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	old := authenticated.Load()

	authenticated.Store(payload.Authenticated)

	if old != payload.Authenticated {
		authUpdateTime.Store(time.Now().UnixMilli())
		signalAuthenticationChanged()
	}

	w.Header().Set("Content-Type", "application/json")

	_ = json.NewEncoder(w).Encode(map[string]bool{
		"authenticated": authenticated.Load(),
	})
}

func (o *HttpServer) GetAuthenticated(w http.ResponseWriter, r *http.Request) {
	var err error
	var fromTime int64

	_fromTime := r.URL.Query().Get("t")

	if _fromTime != "" {
		fromTime, err = strconv.ParseInt(_fromTime, 10, 64)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	updateTime := authUpdateTime.Load()

	if fromTime > 0 {
		if fromTime >= updateTime {

			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()

			authSignalMu.Lock()
			signal := authSignal
			authSignalMu.Unlock()

			select {
			case <-ticker.C:
				http.Error(w, "", http.StatusRequestTimeout)
				return

			case <-signal:
			}

			updateTime = authUpdateTime.Load()
		}
	}

	w.Header().Set("Content-Type", "application/json")

	resp := map[string]interface{}{
		"authenticated": authenticated.Load(),
		"update_time":   updateTime,
	}

	_ = json.NewEncoder(w).Encode(resp)
}

func (o *HttpServer) QRReset(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower("11111111-1111-1111-1111-111111111110")

	val, ok := o.QRCodes.Load(id)
	if !ok {
		http.Error(w, "qrcode not found", http.StatusNotFound)
		return
	}

	qrcode := val.(*QRCode)

	// wipe the "image"
	qrcode.Source = ""
	qrcode.UpdateTime = time.Now().UnixMilli()

	o.QRCodes.Store(id, qrcode)

	// notify listeners if needed
	if qrcode.signal != nil {
		close(qrcode.signal)
		qrcode.signal = make(chan struct{})
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("reset done"))
}

func (o *HttpServer) _getQRCode(id string) *QRCode {
	var qrcode *QRCode = nil

	if _qrcode, ok := o.QRCodes.Load(id); ok {
		qrcode = _qrcode.(*QRCode)
	}

	return qrcode
}

func (o *HttpServer) FileServer(r chi.Router, path string, root http.FileSystem) {
	r.Get(path, func(w http.ResponseWriter, r *http.Request) {

		rctx := chi.RouteContext(r.Context())
		pathPrefix := strings.TrimSuffix(rctx.RoutePattern(), "/*")

		fs := http.StripPrefix(pathPrefix, http.FileServer(root))
		fs.ServeHTTP(w, r)
	})
}

type QRCodeResponse struct {
	*QRCode
}

func NewQRCodeResponse(qrcode *QRCode) *QRCodeResponse {
	resp := &QRCodeResponse{
		QRCode: qrcode,
	}

	return resp
}

func (o *QRCodeResponse) Render(w http.ResponseWriter, r *http.Request) error {
	w.Header().Add("Access-Control-Allow-Origin", "*")
	return nil
}

