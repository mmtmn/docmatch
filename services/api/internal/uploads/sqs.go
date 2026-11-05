package uploads

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// SQSAPI is the slice of the SQS client the consumer uses (fakes in tests).
type SQSAPI interface {
	ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, opts ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, in *sqs.DeleteMessageInput, opts ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

// VerdictApplier is what the consumer needs from the service.
type VerdictApplier interface {
	ApplyVerdict(ctx context.Context, v Verdict) (Upload, error)
}

// SQSConsumer long-polls the verdict queue (DM_SCAN_QUEUE_URL) and applies
// each message. Malformed messages are dropped (and logged); messages whose
// application fails stay in the queue and come back after the visibility
// timeout, so a database hiccup never loses a verdict.
type SQSConsumer struct {
	Client   SQSAPI
	QueueURL string
	Applier  VerdictApplier
	Logger   *slog.Logger
	// WaitSeconds is the long-poll duration (default 20).
	WaitSeconds int32
	// Visibility is how long a received message stays hidden (default 60s).
	Visibility int32
}

// NewSQSConsumer builds the consumer with the default AWS client.
func NewSQSConsumer(ctx context.Context, region, queueURL string, applier VerdictApplier, logger *slog.Logger) (*SQSConsumer, error) {
	if queueURL == "" {
		return nil, errors.New("uploads: DM_SCAN_QUEUE_URL vazia")
	}
	opts := []func(*awsconfig.LoadOptions) error{}
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("uploads: configuração AWS: %w", err)
	}
	return &SQSConsumer{Client: sqs.NewFromConfig(cfg), QueueURL: queueURL, Applier: applier, Logger: logger}, nil
}

// Run polls until ctx is done. Transient receive errors back off briefly.
func (c *SQSConsumer) Run(ctx context.Context) error {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	wait := c.WaitSeconds
	if wait <= 0 {
		wait = 20
	}
	visibility := c.Visibility
	if visibility <= 0 {
		visibility = 60
	}
	c.Logger.Info("consumidor de vereditos iniciado")
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		out, err := c.Client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(c.QueueURL), MaxNumberOfMessages: 10, WaitTimeSeconds: wait, VisibilityTimeout: visibility,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount},
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.Logger.Warn("receber da fila falhou", slog.String("err", err.Error()))
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		for _, m := range out.Messages {
			c.handle(ctx, m)
		}
	}
}

// handle applies one message; it returns nothing because the outcome is
// expressed by deleting (done or poison) or leaving (retry) the message.
func (c *SQSConsumer) handle(ctx context.Context, m types.Message) {
	body := aws.ToString(m.Body)
	v, err := ParseVerdict([]byte(body))
	if err != nil {
		c.Logger.Error("veredito malformado descartado", slog.String("err", err.Error()), slog.String("message_id", aws.ToString(m.MessageId)))
		c.delete(ctx, m)
		return
	}
	if _, err := c.Applier.ApplyVerdict(ctx, v); err != nil {
		if errors.Is(err, ErrUploadNotFound) {
			c.Logger.Warn("veredito para upload desconhecido descartado", slog.String("upload_id", v.UploadID))
			c.delete(ctx, m)
			return
		}
		c.Logger.Warn("aplicar veredito falhou; a mensagem volta à fila", slog.String("upload_id", v.UploadID), slog.String("err", err.Error()))
		return
	}
	c.delete(ctx, m)
}

func (c *SQSConsumer) delete(ctx context.Context, m types.Message) {
	if _, err := c.Client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(c.QueueURL), ReceiptHandle: m.ReceiptHandle}); err != nil {
		c.Logger.Warn("apagar mensagem da fila falhou", slog.String("err", err.Error()))
	}
}
