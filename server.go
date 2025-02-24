package jsonhttp

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	log "github.com/imchuncai/file-log"
	"github.com/lib/pq"
	"gopl.io/ch12/params"
)

var MAX_TRY int
var LOGGER log.Logger

const DEFAULT_MAX_MEMORY = 32 << 20 // 32 MB

// maxTry is only works with postgres
func Listen(address string, maxTry int, logger log.Logger) {
	if maxTry <= 0 {
		panic(fmt.Errorf("json-http: listen %s failed, maxTry must be positive", address))
	}
	MAX_TRY = maxTry
	LOGGER = logger

	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		// TODO: check this will trigger
		Log(log.INFO, (<-c).String())
		os.Exit(5)
	}()
	Log(log.INFO, "json-http: start listen "+address)
	Must(http.ListenAndServe(address, nil))
}

func Must(err error) {
	if err != nil {
		panic(err)
	}
}

func Log(v ...any) {
	LOGGER.Println(v...)
}

type RequestInterface interface {
	Req() *http.Request
	Res() http.ResponseWriter
	IP() string
}

type request interface {
	Request | RequestGet | RequestForm
	RequestInterface
}

type requestPTR[T request] interface {
	from(w http.ResponseWriter, r *http.Request) error
	*T
}

type response interface {
	Response | ResponseFile
	do(w http.ResponseWriter, r *http.Request) error
}

// RP seems stupid
func Handle[R request, RP requestPTR[R], RES response](pattern string, handler func(req R) RES) {
	http.Handle(pattern, wrapHandler[R, RP](handler))
}

func HandleSSE(pattern string, handler func(req RequestSSE)) {
	http.Handle(pattern, wrapHandlerSSE(handler))
}

func HandleFunc(pattern string, handler func(w http.ResponseWriter, r *http.Request)) {
	http.Handle(pattern, wrapHandlerFunc(handler))
}

func wrapHandler[R request, RP requestPTR[R], RES response](handler func(req R) RES) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req R
		err := RP(&req).from(w, r)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			Log(log.ERROR, fmt.Errorf("parse request failed: %w", err))
			return
		}
		for try := MAX_TRY; try > 0; try-- {
			if !handle(handler, req) {
				return
			}
		}
		fmt.Fprint(w, `{"ok":false,"msg":"Server is busy, please try later!"}`)
	}
}

func wrapHandlerSSE(handler func(req RequestSSE)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() { doRecover(recover(), w) }()
		var req RequestSSE
		req.from(w, r)
		handler(req)
	}
}

func wrapHandlerFunc(handler func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() { doRecover(recover(), w) }()
		handler(w, r)
	}
}

func handle[RT request, WT response](handler func(r RT) WT, r RT) (retry bool) {
	defer func() { retry = doRecover(recover(), r.Res()) }()
	res := handler(r)
	err := res.do(r.Res(), r.Req())
	if err != nil {
		Log(log.ERROR, err)
	}
	return false
}

func doRecover(recovered interface{}, w http.ResponseWriter) (retry bool) {
	switch err := recovered.(type) {
	case nil:
	case *pq.Error:
		if err.Code == "40001" || err.Code == "55P03" {
			Log(log.WARN, err, string(debug.Stack()))
			return true
		}
		w.WriteHeader(http.StatusInternalServerError)
		Log(log.ERROR, err, string(debug.Stack()))
	case ErrorWithCode:
		w.WriteHeader(err.HTTPResponseStatusCode)
		Log(log.WARN, err, string(debug.Stack()))
	default:
		w.WriteHeader(http.StatusInternalServerError)
		Log(log.ERROR, err, string(debug.Stack()))
	}
	return false
}

type commonRequest struct {
	w  http.ResponseWriter
	r  *http.Request
	ip string
}

func (r commonRequest) Req() *http.Request {
	return r.r
}

func (r commonRequest) Res() http.ResponseWriter {
	return r.w
}

func (r commonRequest) IP() string {
	return r.ip
}

func (req *commonRequest) From(w http.ResponseWriter, r *http.Request) {
	req.w = w
	req.r = r
	if r.RemoteAddr != "" {
		req.ip = strings.Split(r.RemoteAddr, ":")[0]
	}
}

// Request is http post json request structure
type Request struct {
	commonRequest
	Unmarshal func(v any)
}

func (req *Request) from(w http.ResponseWriter, r *http.Request) error {
	req.commonRequest.From(w, r)

	data, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("read request body failed: %w", err)
	}
	req.Unmarshal = func(v any) {
		MustWithHTTPCode(json.Unmarshal(data, v), http.StatusBadRequest)
	}
	return nil
}

type RequestGet struct {
	commonRequest
	Unmarshal func(v any)
}

func (req *RequestGet) from(w http.ResponseWriter, r *http.Request) error {
	req.commonRequest.From(w, r)
	req.Unmarshal = func(v interface{}) {
		MustWithHTTPCode(params.Unpack(r, v), http.StatusBadRequest)
	}
	return nil
}

type RequestForm struct {
	commonRequest
	Data *multipart.Form
}

func (req *RequestForm) from(w http.ResponseWriter, r *http.Request) error {
	req.commonRequest.From(w, r)
	err := r.ParseMultipartForm(DEFAULT_MAX_MEMORY)
	if err != nil {
		return fmt.Errorf("parse request multipart form failed: %w", err)
	}
	req.Data = r.MultipartForm
	return nil
}

type RequestSSE struct {
	commonRequest
	Unmarshal func(v interface{})
	Write     func(v []byte)
}

func (req *RequestSSE) from(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	req.commonRequest.From(w, r)
	req.Unmarshal = func(v interface{}) {
		MustWithHTTPCode(params.Unpack(r, v), http.StatusBadRequest)
	}
	req.Write = func(v []byte) {
		rc := http.NewResponseController(w)
		_, err := w.Write(v)
		Must(err)
		Must(rc.Flush())
	}
}

func panicWithHTTPCode(err error, httpResponseStatusCode int) {
	panic(ErrorWithCode{httpResponseStatusCode, err})
}

func MustWithHTTPCode(err error, httpResponseStatusCode int) {
	if err != nil {
		panicWithHTTPCode(err, httpResponseStatusCode)
	}
}

// ErrorWithCode is an error with http response status code
type ErrorWithCode struct {
	HTTPResponseStatusCode int
	OriginError            error
}

func (e ErrorWithCode) Error() string {
	return fmt.Sprintf("HTTPResponseStatusCode:%d OriginError:%v", e.HTTPResponseStatusCode, e.OriginError)
}

type Response struct {
	Success bool        `json:"success"`
	Code    int         `json:"code"`
	Msg     string      `json:"msg"`
	Data    interface{} `json:"data"`
}

func (res Response) do(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Content-Type", "application/json")
	resJSONByte, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("json marshal response failed: %w", err)
	}

	// TODO: check for short write
	_, err = fmt.Fprint(w, string(resJSONByte))
	if err != nil {
		return fmt.Errorf("write to http.ResponseWriter failed: %w", err)
	}

	return nil
}

type ResponseFile struct {
	FileName string
	Content  io.ReadSeeker
	Modtime  time.Time
}

func (res ResponseFile) do(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Content-Disposition", "attachment; filename="+res.FileName)
	http.ServeContent(w, r, res.FileName, res.Modtime, res.Content)
	return nil
}

// Success generate success response
func Success(data interface{}) Response {
	return Response{Success: true, Data: data}
}

type FailCode interface {
	Int() int
	Message() string
}

// Fail generate fail response
func Fail(code FailCode) Response {
	return Response{Success: false, Msg: code.Message(), Code: code.Int()}
}

func FailWithMsg(code FailCode, msg string) Response {
	return Response{Success: false, Msg: msg, Code: code.Int()}
}

func FailWithHTTPCode(err error, httpStatusCode int) {
	panicWithHTTPCode(err, http.StatusForbidden)
}

// Forbidden panic a ErrorWithCode error
func Forbidden(err error) {
	panicWithHTTPCode(err, http.StatusForbidden)
}

func BadRequest(err error) {
	panicWithHTTPCode(err, http.StatusBadRequest)
}
