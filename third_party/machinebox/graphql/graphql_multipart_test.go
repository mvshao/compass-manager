package graphql

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/matryer/is"
)

func TestWithClient(t *testing.T) {
	is := is.New(t)
	var calls int
	testClient := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			resp := &http.Response{
				Body: io.NopCloser(strings.NewReader(`{"data":{"key":"value"}}`)),
			}
			return resp, nil
		}),
	}

	ctx := context.Background()
	client := NewClient("", WithHTTPClient(testClient), UseMultipartForm())

	req := NewRequest(``)
	err := client.Run(ctx, req, nil)
	if err != nil {
		is.NoErr(err)
	}

	is.Equal(calls, 1) // calls
}

func TestDoUseMultipartForm(t *testing.T) {
	is := is.New(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		is.Equal(r.Method, http.MethodPost)
		query := r.FormValue("query")
		is.Equal(query, `query {}`)
		_, err := io.WriteString(w, `{
			"data": {
				"something": "yes"
			}
		}`)
		if err != nil {
			is.NoErr(err)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	client := NewClient(srv.URL, UseMultipartForm())

	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	var responseData map[string]interface{}
	err := client.Run(ctx, &Request{q: "query {}"}, &responseData)
	is.NoErr(err)
	is.Equal(calls, 1) // calls
	is.Equal(responseData["something"], "yes")
}

func TestImmediatelyCloseReqBody(t *testing.T) {
	is := is.New(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		is.Equal(r.Method, http.MethodPost)
		query := r.FormValue("query")
		is.Equal(query, `query {}`)
		_, err := io.WriteString(w, `{
			"data": {
				"something": "yes"
			}
		}`)
		if err != nil {
			is.NoErr(err)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	client := NewClient(srv.URL, ImmediatelyCloseReqBody(), UseMultipartForm())

	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	var responseData map[string]interface{}
	err := client.Run(ctx, &Request{q: "query {}"}, &responseData)
	is.NoErr(err)
	is.Equal(calls, 1) // calls
	is.Equal(responseData["something"], "yes")
}

func TestDoErr(t *testing.T) {
	is := is.New(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		is.Equal(r.Method, http.MethodPost)
		query := r.FormValue("query")
		is.Equal(query, `query {}`)
		_, err := io.WriteString(w, `{
			"errors": [{
				"message": "Something went wrong"
			}]
		}`)
		if err != nil {
			is.NoErr(err)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	client := NewClient(srv.URL, UseMultipartForm())

	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	var responseData map[string]interface{}
	err := client.Run(ctx, &Request{q: "query {}"}, &responseData)
	is.True(err != nil)
	is.Equal(err.Error(), "graphql: Something went wrong")
}

func TestDoServerErr(t *testing.T) {
	is := is.New(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		is.Equal(r.Method, http.MethodPost)
		query := r.FormValue("query")
		is.Equal(query, `query {}`)
		w.WriteHeader(http.StatusInternalServerError)
		_, err := io.WriteString(w, `Internal Server Error`)
		if err != nil {
			is.NoErr(err)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	client := NewClient(srv.URL, UseMultipartForm())

	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	var responseData map[string]interface{}
	err := client.Run(ctx, &Request{q: "query {}"}, &responseData)
	is.Equal(err.Error(), "graphql: server returned a non-200 status code: 500")
}

func TestDoBadRequestErr(t *testing.T) {
	is := is.New(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		is.Equal(r.Method, http.MethodPost)
		query := r.FormValue("query")
		is.Equal(query, `query {}`)
		w.WriteHeader(http.StatusBadRequest)
		_, err := io.WriteString(w, `{
			"errors": [{
				"message": "miscellaneous message as to why the the request was bad"
			}]
		}`)
		if err != nil {
			is.NoErr(err)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	client := NewClient(srv.URL, UseMultipartForm())

	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	var responseData map[string]interface{}
	err := client.Run(ctx, &Request{q: "query {}"}, &responseData)
	is.Equal(err.Error(), "graphql: miscellaneous message as to why the the request was bad")
}

func TestDoNoResponse(t *testing.T) {
	is := is.New(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		is.Equal(r.Method, http.MethodPost)
		query := r.FormValue("query")
		is.Equal(query, `query {}`)
		_, err := io.WriteString(w, `{
					"data": {
						"something": "yes"
					}
				}`)
		if err != nil {
			is.NoErr(err)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	client := NewClient(srv.URL, UseMultipartForm())

	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	err := client.Run(ctx, &Request{q: "query {}"}, nil)
	is.NoErr(err)
	is.Equal(calls, 1) // calls
}

func TestQuery(t *testing.T) {
	is := is.New(t)

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		query := r.FormValue("query")
		is.Equal(query, "query {}")
		is.Equal(r.FormValue("variables"), `{"username":"matryer"}`+"\n")
		_, err := io.WriteString(w, `{"data":{"value":"some data"}}`)
		is.NoErr(err)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	client := NewClient(srv.URL, UseMultipartForm())

	req := NewRequest("query {}")
	req.Var("username", "matryer")

	// check variables
	is.True(req != nil)
	is.Equal(req.vars["username"], "matryer")

	var resp struct {
		Value string
	}
	err := client.Run(ctx, req, &resp)
	is.NoErr(err)
	is.Equal(calls, 1)

	is.Equal(resp.Value, "some data")
}

func TestFile(t *testing.T) {
	is := is.New(t)

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		file, header, err := r.FormFile("file")
		is.NoErr(err)
		defer func(file multipart.File) {
			err = file.Close()
			if err != nil {
				is.NoErr(err)
			}
		}(file)
		is.Equal(header.Filename, "filename.txt")

		b, err := io.ReadAll(file)
		is.NoErr(err)
		is.Equal(string(b), `This is a file`)

		_, err = io.WriteString(w, `{"data":{"value":"some data"}}`)
		is.NoErr(err)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	client := NewClient(srv.URL, UseMultipartForm())
	f := strings.NewReader(`This is a file`)
	req := NewRequest("query {}")
	req.File("file", "filename.txt", f)
	err := client.Run(ctx, req, nil)
	is.NoErr(err)
}

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestHideAuthInMultipartForm(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.WriteString(w, `{
			"data": {
				"something": "yes"
			}
		}`)
		if err != nil {
			is.NoErr(err)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	client := NewClient(srv.URL, UseMultipartForm())

	var cout bytes.Buffer
	client.Log = func(s string) {
		_, err := cout.WriteString(s)
		is.NoErr(err)
	}

	header := make(http.Header)
	header["Authorization"] = []string{"some secret key", "another secret key"}
	req := Request{
		q:      "query {}",
		Header: header,
	}

	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	var responseData map[string]interface{}
	err := client.Run(ctx, &req, &responseData)
	is.NoErr(err)
	is.Equal(responseData["something"], "yes")
	is.True(!strings.Contains(cout.String(), "secret key"))
}
