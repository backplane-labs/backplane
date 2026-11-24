package temporal

import (
	"context"
	"fmt"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"

	inftemporal "github.com/backplane-labs/backplane/pkg/backplane/infra/temporal"
)

type guardCodec struct {
	check func([]*commonpb.Payload) error
}

func (guardCodec) Encode(payloads []*commonpb.Payload) ([]*commonpb.Payload, error) {
	return payloads, nil
}

func (g guardCodec) Decode(payloads []*commonpb.Payload) ([]*commonpb.Payload, error) {
	if err := g.check(payloads); err != nil {
		return nil, fmt.Errorf("temporal worker payload: %w", converter.NewWorkflowTaskFailureError(err))
	}

	return payloads, nil
}

// workerClient shares the frontend connection but not the ordinary client's
// converter. A codec outside a sealed DataConverter would see ciphertext.
func (c *Client) workerClient(
	ctx context.Context, conn client.Client, opts client.Options, guard func([]*commonpb.Payload) error,
) (client.Client, error) {
	var codecs []converter.PayloadCodec

	if c.p.Conn.Encryption.Enabled() {
		seal, err := inftemporal.NewCodec(c.p.Conn.Encryption)
		if err != nil {
			return nil, fmt.Errorf("temporal worker: %w", err)
		}

		codecs = append(codecs, seal.Strict(c.p.Conn.RequireEncryption))
	}

	codecs = append(codecs, guardCodec{check: guard})
	opts.DataConverter = converter.NewCodecDataConverter(converter.GetDefaultDataConverter(), codecs...)

	ctx, cancel := context.WithTimeout(ctx, c.p.Conn.DialTimeout)
	defer cancel()

	guarded, err := client.NewClientFromExistingWithContext(ctx, conn, opts)
	if err != nil {
		return nil, fmt.Errorf("temporal: guard worker connection: %w", err)
	}

	return guarded, nil
}
