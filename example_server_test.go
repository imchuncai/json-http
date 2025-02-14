package jsonhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

func ExampleHandle() {
	Handle("/hello", func(req Request) Response {
		var reqData struct {
			Name string `json:"name"`
		}
		req.Unmarshal(&reqData)
		if reqData.Name == "" {
			MustWithHTTPCode(errors.New("name is empty"), http.StatusBadRequest)
		}

		data := struct {
			Message string `json:"message"`
		}{
			fmt.Sprintf("Hello, %s.", reqData.Name),
		}
		return Success(data)
	})
	go Listen(":8080", 3, log.Default())
	time.Sleep(time.Second)

	res, err := http.Post("http://localhost:8080/hello", "", bytes.NewBufferString(`{"name": "imchuncai"}`))
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()

	data, err := io.ReadAll(res.Body)
	if err != nil {
		log.Fatal(err)
	}
	var resData struct {
		Success bool   `json:"success"`
		Code    int    `json:"code"`
		Msg     string `json:"msg"`
		Data    struct {
			Message string `json:"message"`
		} `json:"data"`
	}
	err = json.Unmarshal(data, &resData)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(resData)
	// Output: {true 0  {Hello, imchuncai.}}
}

func ExampleHandleSSE() {
	HandleSSE("/hello1", func(req RequestSSE) {
		var reqData struct {
			Name string `json:"name"`
		}
		req.Unmarshal(&reqData)
		if reqData.Name == "" {
			MustWithHTTPCode(errors.New("name is empty"), http.StatusBadRequest)
		}

		req.Write([]byte("Hello, "))
		req.Write([]byte(reqData.Name + "."))
	})
	go Listen(":8081", 3, log.Default())
	time.Sleep(time.Second)

	res, err := http.Get("http://localhost:8080/hello1?name=imchuncai")
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()

	data, err := io.ReadAll(res.Body)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(data))
	// Output: Hello, imchuncai.
}
