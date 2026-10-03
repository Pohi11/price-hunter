package notify

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/smithy-go"
)

type fakeSES struct {
	err error
	in  *sesv2.SendEmailInput
}

func (f *fakeSES) SendEmail(_ context.Context, in *sesv2.SendEmailInput, _ ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	f.in = in
	if f.err != nil {
		return nil, f.err
	}
	return &sesv2.SendEmailOutput{MessageId: aws.String("0100018f-abc")}, nil
}

func TestSESSenderBuildsRequest(t *testing.T) {
	f := &fakeSES{}
	s := SESSender{Client: f, From: "alerts@pricehunter.example", ConfigurationSet: "pricehunter-dev"}
	id, err := s.Send(context.Background(), Email{To: "me@example.com", Subject: "Price alert", Text: "t", HTML: "<p>h</p>"})
	if err != nil || id != "0100018f-abc" {
		t.Fatalf("send: %q %v", id, err)
	}
	if aws.ToString(f.in.FromEmailAddress) != "alerts@pricehunter.example" || f.in.Destination.ToAddresses[0] != "me@example.com" ||
		aws.ToString(f.in.ConfigurationSetName) != "pricehunter-dev" || aws.ToString(f.in.Content.Simple.Body.Html.Data) != "<p>h</p>" {
		t.Fatalf("request: %+v", f.in)
	}
}

func TestSESErrorClassification(t *testing.T) {
	cases := []struct {
		code      string
		permanent bool
	}{
		{"MessageRejected", true}, // e.g. unverified recipient in the SES sandbox
		{"MailFromDomainNotVerifiedException", true},
		{"TooManyRequestsException", false},
		{"LimitExceededException", false},
		{"AccountSuspendedException", false},
	}
	for _, c := range cases {
		f := &fakeSES{err: &smithy.GenericAPIError{Code: c.code, Message: "x"}}
		_, err := SESSender{Client: f, From: "a@b.c"}.Send(context.Background(), Email{To: "x@y.z"})
		var perm *PermanentError
		if errors.As(err, &perm) != c.permanent {
			t.Errorf("%s: permanent = %v, want %v", c.code, !c.permanent, c.permanent)
		}
	}
}
