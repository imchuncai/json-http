package jsonhttp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"testing"
	"time"
)

func SetEnvironment() {
	MAX_TRY = 1
	LOGGER = log.Default()
}

func JsonEqual(a, b []byte) (bool, error) {
	var aa, bb interface{}
	err := json.Unmarshal(a, &aa)
	if err != nil {
		return false, fmt.Errorf("json unmarshal a failed: %w", err)
	}
	err = json.Unmarshal(b, &bb)
	if err != nil {
		return false, fmt.Errorf("json unmarshal b failed: %w", err)
	}
	return reflect.DeepEqual(aa, bb), nil
}

type RequestDataInterface interface {
	request(url string) (resp *http.Response, err error)
	data() RequestData
}

type RequestData struct {
	Type        string `json:"type" http:"type"`
	FileName    string `json:"file_name" http:"file_name"`
	FileContent string `json:"file_content" http:"file_content"`
}

func (d RequestData) data() RequestData {
	return d
}

type ReqData struct {
	RequestData
}

func (d ReqData) request(url string) (resp *http.Response, err error) {
	data, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	return http.Post(url, "", bytes.NewReader(data))
}

type ReqGetData struct {
	RequestData
}

func (d ReqGetData) request(url string) (resp *http.Response, err error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("http new request failed: %w", err)
	}
	q := req.URL.Query()
	q.Add("type", d.Type)
	q.Add("file_name", d.FileName)
	q.Add("file_content", d.FileContent)
	req.URL.RawQuery = q.Encode()
	client := &http.Client{}
	return client.Do(req)
}

type ReqFormData struct {
	RequestData
}

func (d ReqFormData) request(url string) (resp *http.Response, err error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormField("type")
	if err != nil {
		return nil, fmt.Errorf("create form field failed: %w", err)
	}
	_, err = part.Write([]byte(d.Type))
	if err != nil {
		return nil, fmt.Errorf("write to form field failed: %w", err)
	}

	part, err = writer.CreateFormFile("file", d.FileName)
	if err != nil {
		return nil, fmt.Errorf("create form file field failed: %w", err)
	}
	_, err = part.Write([]byte(d.FileContent))
	if err != nil {
		return nil, fmt.Errorf("write to form file field failed: %w", err)
	}

	writer.Close()

	request, err := http.NewRequest("POST", url, body)
	if err != nil {
		return nil, fmt.Errorf("new http request failed: %w", err)
	}
	request.Header.Add("Content-Type", writer.FormDataContentType())

	client := &http.Client{}
	return client.Do(request)
}

var reqData = ReqData{RequestData{"JSON", "JSON-test-file", "This is a JSON test file."}}
var reqGetData = ReqGetData{RequestData{"GET", "GET-test-file", "This is a GET test file."}}
var reqFormData = ReqFormData{RequestData{"FORM", "FORM-test-file", "This is a FORM test file."}}

func test[R request, RP requestPTR[R], RES response](t *testing.T) {
	ts := httptest.NewServer(wrapHandler[R, RP](func(req R) (res RES) {
		var reqData RequestData
		if __req, ok := interface{}(req).(Request); ok {
			__req.Unmarshal(&reqData)
		} else if __req, ok := interface{}(req).(RequestGet); ok {
			__req.Unmarshal(&reqData)
		} else if __req, ok := interface{}(req).(RequestForm); ok {
			reqData.Type = __req.Data.Value["type"][0]
			reqData.FileName = __req.Data.File["file"][0].Filename
			file, err := __req.Data.File["file"][0].Open()
			Must(err)
			data, err := io.ReadAll(file)
			Must(err)
			reqData.FileContent = string(data)
		}

		if __res, ok := interface{}(&res).(*Response); ok {
			*__res = Success(reqData)
		} else if __res, ok := interface{}(&res).(*ResponseFile); ok {
			*__res = ResponseFile{
				FileName: reqData.FileName,
				Content:  bytes.NewReader([]byte(reqData.FileContent)),
				Modtime:  time.Now(),
			}
		}
		return
	}))
	defer ts.Close()

	var req RequestDataInterface
	var res *http.Response
	var err error
	var r R
	if _, ok := interface{}(r).(Request); ok {
		req = reqData
	} else if _, ok := interface{}(r).(RequestGet); ok {
		req = reqGetData
	} else if _, ok := interface{}(r).(RequestForm); ok {
		req = reqFormData
	}
	res, err = req.request(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var __res RES
	if _, ok := interface{}(__res).(Response); ok {
		var resData []byte
		resData, err = json.Marshal(Response{true, 0, "", req})
		if err != nil {
			t.Fatal(err)
		}
		equal, err := JsonEqual(resData, data)
		if err != nil {
			t.Fatal(err)
		}
		if !equal {
			t.Fatalf("want data: %s got %s", string(resData), string(data))
		}
	} else if _, ok := interface{}(__res).(ResponseFile); ok {
		reg, err := regexp.Compile(`filename=(\S+\b)`)
		if err != nil {
			t.Fatal(err)
		}
		contentDisposition := res.Header.Get("Content-Disposition")
		match := reg.FindStringSubmatch(contentDisposition)
		if len(match) != 2 {
			t.Fatalf("want match size 2")
		}
		fileName := match[1]

		if req.data().FileName != fileName {
			t.Fatalf("want file name: %s got %s", req.data().FileName, fileName)
		}
		if req.data().FileContent != string(data) {
			t.Fatalf("want data: %s got %s", req.data().FileContent, string(data))
		}
	}
}

func TestHandle(t *testing.T) {
	SetEnvironment()
	test[Request, *Request, Response](t)
	test[Request, *Request, ResponseFile](t)
	test[RequestGet, *RequestGet, Response](t)
	test[RequestGet, *RequestGet, ResponseFile](t)
	test[RequestForm, *RequestForm, Response](t)
	test[RequestForm, *RequestForm, ResponseFile](t)
}

func TestHandleSSE(t *testing.T) {
	SetEnvironment()

	message := "hello, world."
	messageN := 1000
	handler := wrapHandlerSSE(func(req RequestSSE) {
		for range messageN {
			req.Write([]byte(message + "\n\n"))
		}
	})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	res, err := http.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	var scanner = bufio.NewScanner(res.Body)
	scanner.Split(func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}
		if i := bytes.Index(data, []byte("\n\n")); i >= 0 {
			return i + 2, data[0:i], nil
		}
		if atEOF {
			return len(data), data, nil
		}
		return 0, nil, nil
	})

	n := 0
	for scanner.Scan() {
		data := scanner.Bytes()
		for i, v := range data {
			want := message[(n+i)%len(message)]
			if want != v {
				t.Fatalf("%d: want character: %c got %c", n+i, want, v)
			}
		}
		n += len(data)
	}
	if n != len(message)*messageN {
		t.Fatalf("wrong message length: want %d got %d", len(message)*messageN, n)
	}
}

func TestHandleFunc(t *testing.T) {
	SetEnvironment()

	hello := []byte("Hello, world.\n")
	handler := wrapHandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write(hello)
		Must(err)
	})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	res, err := http.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}

	if string(hello) != string(data) {
		t.Fatalf("wrong response data: want %s got %s", string(hello), string(data))
	}
}
