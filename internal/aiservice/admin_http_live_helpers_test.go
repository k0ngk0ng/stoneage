package aiservice

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func adminLiveHTTPLogin(t *testing.T, baseURL, username, password string) (*http.Client, string) {
	t.Helper()
	const maxBodyBytes int64 = 1 << 20

	readBody := func(body io.ReadCloser) ([]byte, bool) {
		if body == nil {
			return nil, false
		}
		data, readErr := io.ReadAll(io.LimitReader(body, maxBodyBytes+1))
		closeErr := body.Close()
		return data, readErr == nil && closeErr == nil && int64(len(data)) <= maxBodyBytes
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal("admin live login could not create cookie jar")
	}
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	loginResponse, err := client.PostForm(strings.TrimRight(baseURL, "/")+"/login", url.Values{
		"username": {username},
		"password": {password},
	})
	if err != nil {
		if loginResponse != nil {
			_, _ = readBody(loginResponse.Body)
		}
		t.Fatal("admin live login request failed")
	}
	if loginResponse == nil {
		t.Fatal("admin live login response missing")
	}
	if _, ok := readBody(loginResponse.Body); !ok || loginResponse.StatusCode < http.StatusOK || loginResponse.StatusCode >= http.StatusMultipleChoices {
		t.Fatal("admin live login response invalid")
	}

	pageResponse, err := client.Get(strings.TrimRight(baseURL, "/") + "/ai/profiles")
	if err != nil {
		if pageResponse != nil {
			_, _ = readBody(pageResponse.Body)
		}
		t.Fatal("admin live profile page request failed")
	}
	if pageResponse == nil {
		t.Fatal("admin live profile page response missing")
	}
	pageBody, ok := readBody(pageResponse.Body)
	if !ok || pageResponse.StatusCode != http.StatusOK {
		t.Fatal("admin live profile page response invalid")
	}
	csrfMatch := regexp.MustCompile(`data-csrf="([^"]+)"`).FindSubmatch(pageBody)
	if len(csrfMatch) != 2 || len(csrfMatch[1]) == 0 {
		t.Fatal("admin live profile page did not provide CSRF token")
	}
	return client, string(csrfMatch[1])
}
