package ucon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestHttpRWDI(t *testing.T) {
	b, _ := MakeMiddlewareTestBed(t, HTTPRWDI(), func(w http.ResponseWriter, r *http.Request) {
		if w == nil {
			t.Errorf("unexpected: %v", w)
		}
		if r == nil {
			t.Errorf("unexpected: %v", w)
		}
	}, nil)
	err := b.Next()
	if err != nil {
		t.Fatal(err)
	}
}

func TestContextDI(t *testing.T) {
	b, _ := MakeMiddlewareTestBed(t, ContextDI(), func(c context.Context) {
		if c == nil {
			t.Errorf("unexpected: %v", c)
		}
	}, nil)
	err := b.Next()
	if err != nil {
		t.Fatal(err)
	}
}

type TargetOfRequestObjectMapper struct {
	ID     int    `json:"id"`
	Offset int    `json:"offset"`
	Text   string `json:"text"`
}

func TestRequestObjectMapper(t *testing.T) {
	b, _ := MakeMiddlewareTestBed(t, RequestObjectMapper(), func(req *TargetOfRequestObjectMapper) {
		if req.ID != 5 {
			t.Errorf("unexpected: %v", req.ID)
		}
		if req.Offset != 10 {
			t.Errorf("unexpected: %v", req.Offset)
		}
		if req.Text != "Hi!" {
			t.Errorf("unexpected: %v", req.Text)
		}
	}, &BubbleTestOption{
		Method: "POST",
		URL:    "/api/todo/{id}?offset=10&limit=3",
		Body:   strings.NewReader("{\"text\":\"Hi!\"}"),
	})
	b.Context = context.WithValue(b.Context, PathParameterKey, map[string]string{
		"id": "5",
	})
	err := b.Next()
	if err != nil {
		t.Fatal(err)
	}
}

func TestResponseMapperWithBubbleReturnsError(t *testing.T) {
	b, mux := MakeMiddlewareTestBed(t, ResponseMapper(), func() {}, nil)

	mux.Middleware(func(b *Bubble) error {
		return errors.New("strange error")
	})

	err := b.Next()
	if err != nil {
		t.Fatal(err)
	}

	body := b.W.(*httptest.ResponseRecorder).Body.String()
	if body != "{\"code\":500,\"message\":\"strange error\"}" {
		t.Errorf("unexpected: %v", body)
	}
}

func TestResponseMapperWithHandlerReturnsError(t *testing.T) {
	b, _ := MakeMiddlewareTestBed(t, ResponseMapper(), func() error {
		return errors.New("strange error")
	}, nil)

	err := b.Next()
	if err != nil {
		t.Fatal(err)
	}

	body := b.W.(*httptest.ResponseRecorder).Body.String()
	if body != "{\"code\":500,\"message\":\"strange error\"}" {
		t.Errorf("unexpected: %v", body)
	}
}

func TestRequestObjectMapperWithWrongTypedPathParameter(t *testing.T) {
	b, _ := MakeMiddlewareTestBed(t, RequestObjectMapper(), func(req *TargetOfRequestObjectMapper) {
		if req.ID != 0 {
			t.Errorf("unexpected: %v", req.ID)
		}
	}, &BubbleTestOption{
		Method: "POST",
		URL:    "/api/todo/{id}",
		Body:   strings.NewReader("{\"text\": \"Hi!\"}"),
	})
	b.Context = context.WithValue(b.Context, PathParameterKey, map[string]string{
		"id": "test", // not a number -> zero
	})
	err := b.Next()
	if err != nil {
		t.Fatal(err)
	}
}

type TargetOfResponseMapper struct {
	ID     int    `json:"id"`
	Offset int    `json:"offset"`
	Text   string `json:"text"`
}

func TestResponseMapperWithHandlerReturnsResponse(t *testing.T) {
	b, _ := MakeMiddlewareTestBed(t, ResponseMapper(), func() *TargetOfResponseMapper {
		return &TargetOfResponseMapper{
			ID:     11,
			Offset: 22,
			Text:   "Hi!",
		}
	}, nil)

	err := b.Next()
	if err != nil {
		t.Fatal(err)
	}

	body := b.W.(*httptest.ResponseRecorder).Body.String()
	if body != "{\"id\":11,\"offset\":22,\"text\":\"Hi!\"}" {
		t.Errorf("unexpected: %v", body)
	}
}

func TestResponseMapperWithHandlerReturnsResponseNil(t *testing.T) {
	b, _ := MakeMiddlewareTestBed(t, ResponseMapper(), func() *TargetOfResponseMapper {
		return nil
	}, nil)

	err := b.Next()
	if err != nil {
		t.Fatal(err)
	}

	body := b.W.(*httptest.ResponseRecorder).Body.String()
	if body != "{}" {
		t.Errorf("unexpected: %v", body)
	}
}

type TargetOfResponseMapperAndHTTPResponseModifier struct {
	ID       int    `json:"id"`
	Password string `json:"password,omitempty"`
}

var _ HTTPResponseModifier = &TargetOfResponseMapperAndHTTPResponseModifier{}

func (v *TargetOfResponseMapperAndHTTPResponseModifier) Handle(b *Bubble) error {
	v.Password = ""
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}

	b.W.Header().Set("Content-Type", "application/json; charset=UTF-8")
	b.W.WriteHeader(http.StatusOK)
	b.W.Write(body)

	return nil
}

func TestResponseMapperWithHandlerReturnsHttpResponseModifier(t *testing.T) {
	b, _ := MakeMiddlewareTestBed(t, ResponseMapper(), func() *TargetOfResponseMapperAndHTTPResponseModifier {
		return &TargetOfResponseMapperAndHTTPResponseModifier{
			ID:       11,
			Password: "super ultra secret!",
		}
	}, nil)

	err := b.Next()
	if err != nil {
		t.Fatal(err)
	}

	body := b.W.(*httptest.ResponseRecorder).Body.String()
	if body != "{\"id\":11}" {
		t.Errorf("unexpected: %v", body)
	}
}

type ResponseMapperCustomError struct {
	Message string `json:"message"`
}

type ResponseMapperCustomErrorMessage struct {
	Text string `json:"text"`
}

var _ HTTPErrorResponse = &ResponseMapperCustomError{}

func (ce *ResponseMapperCustomError) Error() string {
	return ce.Message
}

func (ce *ResponseMapperCustomError) StatusCode() int {
	return 400
}

func (ce *ResponseMapperCustomError) ErrorMessage() interface{} {
	return &ResponseMapperCustomErrorMessage{Text: ce.Message}
}

func TestResponseMapperWithCustomErrorType(t *testing.T) {
	b, _ := MakeMiddlewareTestBed(t, ResponseMapper(), func() *ResponseMapperCustomError {
		return &ResponseMapperCustomError{
			Message: "Hello from custom error",
		}
	}, nil)

	err := b.Next()
	if err != nil {
		t.Fatal(err)
	}

	rr := b.W.(*httptest.ResponseRecorder)
	if rr.Code != 400 {
		t.Errorf("unexpected: %v", rr.Code)
	}
	body := rr.Body.String()
	if body != "{\"text\":\"Hello from custom error\"}" {
		t.Errorf("unexpected: %v", body)
	}
}

type TargetRequestValidate struct {
	ID int `ucon:"min=3"`
}

type IgnoreRequestValidate struct {
	ID int `ucon:"min=4"`
}


func TestRequestValidator_valid(t *testing.T) {
	b, _ := MakeMiddlewareTestBed(t, RequestValidator(nil), func(req *TargetRequestValidate) {
	}, nil)
	b.Arguments[0] = reflect.ValueOf(&TargetRequestValidate{ID: 3})

	err := b.Next()
	if err != nil {
		t.Fatal(err)
	}

	rr := b.W.(*httptest.ResponseRecorder)
	if rr.Code != 200 {
		t.Errorf("unexpected: %v", rr.Code)
	}
}

func TestRequestValidator_validWithIgnore(t *testing.T) {

	ignoreType := reflect.TypeOf((*IgnoreRequestValidate)(nil))

	b, _ := MakeMiddlewareTestBed(t, RequestValidator(nil, &RequestValidatorOption{IgnoreValidateFunc: func(argT reflect.Type, rv reflect.Value) bool {
		if ignoreType.AssignableTo(argT) {
			return true
		}
		return false
	}}), func(req *TargetRequestValidate, req2 *IgnoreRequestValidate) {
	}, nil)
	b.Arguments[0] = reflect.ValueOf(&TargetRequestValidate{ID: 3})
	b.Arguments[1] = reflect.ValueOf(&IgnoreRequestValidate{ID: 4})

	err := b.Next()
	if err != nil {
		t.Fatal(err)
	}

	rr := b.W.(*httptest.ResponseRecorder)
	if rr.Code != 200 {
		t.Errorf("unexpected: %v", rr.Code)
	}
}

func TestRequestValidator_invalidRequest(t *testing.T) {
	b, _ := MakeMiddlewareTestBed(t, RequestValidator(nil), func(req *TargetRequestValidate) {
	}, nil)
	b.Arguments[0] = reflect.ValueOf(&TargetRequestValidate{ID: 2})

	err := b.Next()
	if err == nil {
		t.Errorf("unexpected: %v", err)
	}
}

func TestCSRFProtect_safeMethodWithoutCSRFToken(t *testing.T) {
	mw, err := CSRFProtect(&CSRFOption{
		Salt: []byte("foobar"),
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := MakeMiddlewareTestBed(t, mw, func() {}, &BubbleTestOption{
		Method: "GET",
		URL:    "/api/tmp",
	})

	err = b.Next()
	if err != nil {
		t.Fatal(err)
	}

	cookie := b.W.Header().Get("Set-Cookie")
	if cookie == "" {
		t.Errorf("unexpected: %s", cookie)
	}
	if !strings.HasPrefix(cookie, "XSRF-TOKEN=") {
		t.Errorf("unexpected: %s", cookie)
	}
}

func TestCSRFProtect_safeMethodWithCSRFToken(t *testing.T) {
	mw, err := CSRFProtect(&CSRFOption{
		Salt: []byte("foobar"),
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := MakeMiddlewareTestBed(t, mw, func() {}, &BubbleTestOption{
		Method: "GET",
		URL:    "/api/tmp",
	})
	csrfToken := "cd4c742bb3f6ba24e0cedc1bae73e25f255b4d0a88f923c82f89f91131bed6c6"
	b.R.Header.Add("Cookie", fmt.Sprintf("XSRF-TOKEN=%s", csrfToken))

	err = b.Next()
	if err != nil {
		t.Fatal(err)
	}

	cookie := b.W.Header().Get("Set-Cookie")
	if cookie != "" {
		t.Errorf("unexpected: %s", cookie)
	}
}

func TestCSRFProtect_validCSRFToken(t *testing.T) {
	mw, err := CSRFProtect(&CSRFOption{
		Salt: []byte("foobar"),
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := MakeMiddlewareTestBed(t, mw, func() {}, &BubbleTestOption{
		Method: "POST",
		URL:    "/api/tmp",
	})
	csrfToken := "cd4c742bb3f6ba24e0cedc1bae73e25f255b4d0a88f923c82f89f91131bed6c6"
	b.R.Header.Add("Cookie", fmt.Sprintf("XSRF-TOKEN=%s", csrfToken))
	b.R.Header.Add("X-XSRF-TOKEN", csrfToken)

	err = b.Next()
	if err != nil {
		t.Fatal(err.(*httpError).Message)
	}
}

func TestCSRFProtect_invalidCSRFToken(t *testing.T) {
	mw, err := CSRFProtect(&CSRFOption{
		Salt: []byte("foobar"),
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := MakeMiddlewareTestBed(t, mw, func() {}, &BubbleTestOption{
		Method: "POST",
		URL:    "/api/tmp",
	})
	csrfToken := "cd4c742bb3f6ba24e0cedc1bae73e25f255b4d0a88f923c82f89f91131bed6c6"
	b.R.Header.Add("Cookie", fmt.Sprintf("XSRF-TOKEN=%s", csrfToken))
	// b.R.Header.Add("X-XSRF-TOKEN", csrfToken)

	err = b.Next()
	if err == nil {
		t.Fatal("unexpected")
	}
	if v := err.(HTTPErrorResponse).StatusCode(); v != http.StatusBadRequest {
		t.Fatalf("unexpected: %v", v)
	}
}

func TestCSRFProtect_mismatchCSRFToken(t *testing.T) {
	mw, err := CSRFProtect(&CSRFOption{
		Salt: []byte("foobar"),
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := MakeMiddlewareTestBed(t, mw, func() {}, &BubbleTestOption{
		Method: "POST",
		URL:    "/api/tmp",
	})
	b.R.Header.Add("Cookie", "XSRF-TOKEN=123")
	b.R.Header.Add("X-XSRF-TOKEN", "abc")

	err = b.Next()
	if err == nil {
		t.Fatal("unexpected")
	}
	if v := err.(HTTPErrorResponse).StatusCode(); v != http.StatusBadRequest {
		t.Fatalf("unexpected: %v", v)
	}
}
