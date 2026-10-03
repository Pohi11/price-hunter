package notify

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"
)

// SESAPI is the SES v2 call used.
type SESAPI interface {
	SendEmail(ctx context.Context, in *sesv2.SendEmailInput, opts ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error)
}

// SESSender delivers email through Amazon SES. Bounces and complaints are
// handled by the account-level suppression list plus the configuration set
// (see infra/modules/app/email.tf).
type SESSender struct {
	Client           SESAPI
	From             string
	ConfigurationSet string
}

func (s SESSender) Send(ctx context.Context, e Email) (string, error) {
	in := &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(s.From),
		Destination:      &types.Destination{ToAddresses: []string{e.To}},
		Content: &types.EmailContent{Simple: &types.Message{
			Subject: &types.Content{Data: aws.String(e.Subject), Charset: aws.String("UTF-8")},
			Body: &types.Body{
				Text: &types.Content{Data: aws.String(e.Text), Charset: aws.String("UTF-8")},
				Html: &types.Content{Data: aws.String(e.HTML), Charset: aws.String("UTF-8")},
			},
		}},
	}
	if s.ConfigurationSet != "" {
		in.ConfigurationSetName = aws.String(s.ConfigurationSet)
	}
	out, err := s.Client.SendEmail(ctx, in)
	if err != nil {
		return "", classifySES(err)
	}
	return aws.ToString(out.MessageId), nil
}

// classifySES separates failures that retrying cannot fix from ones that
// might succeed later.
func classifySES(err error) error {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "MessageRejected", // includes "Email address is not verified" in the SES sandbox
			"MailFromDomainNotVerifiedException", "BadRequestException", "NotFoundException":
			return &PermanentError{Err: fmt.Errorf("ses %s: %s", ae.ErrorCode(), ae.ErrorMessage())}
		}
	}
	// Throttling, TooManyRequests, LimitExceeded, AccountSuspended (may be
	// lifted), network errors: retry later.
	return fmt.Errorf("ses send: %w", err)
}
