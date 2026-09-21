package api

import (
	"net/http"
	"testing"
)

func TestGetValidateToken(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "valid", value: "v=1:t=<synthetic-token>", want: "<synthetic-token>"},
		{name: "missing cookie", wantErr: true},
		{name: "malformed", value: "not-a-validation-cookie", wantErr: true},
		{name: "missing token", value: "v=1:x=value", wantErr: true},
		{name: "empty token", value: "v=1:t=", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := &Session{}
			if test.name != "missing cookie" {
				session.Cookies = []*http.Cookie{{Name: "X-APPLE-WEBAUTH-VALIDATE", Value: test.value}}
			}
			got, err := session.GetValidateToken()
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
			if !test.wantErr && got != test.want {
				t.Fatalf("token = %q, want %q", got, test.want)
			}
		})
	}
}
