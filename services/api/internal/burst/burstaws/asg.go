// Package burstaws is the AWS side of the pool of on-demand workers: the
// Auto Scaling group the scaler resizes. The instance role of the main box
// may call these actions on that one group only (modules/compute, iam.tf).
package burstaws

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"

	"github.com/mmtmn/docmatch/services/api/internal/burst"
)

// API is the subset of the Auto Scaling client the group needs.
type API interface {
	DescribeAutoScalingGroups(ctx context.Context, in *autoscaling.DescribeAutoScalingGroupsInput, opts ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error)
	SetDesiredCapacity(ctx context.Context, in *autoscaling.SetDesiredCapacityInput, opts ...func(*autoscaling.Options)) (*autoscaling.SetDesiredCapacityOutput, error)
	SetInstanceProtection(ctx context.Context, in *autoscaling.SetInstanceProtectionInput, opts ...func(*autoscaling.Options)) (*autoscaling.SetInstanceProtectionOutput, error)
}

// Group is one Auto Scaling group (burst.ASG).
type Group struct {
	API  API
	Name string
}

// Options build a Group.
type Options struct {
	Name   string
	Region string
	// Endpoint and static credentials are for tests; production uses the
	// instance role through the default chain.
	Endpoint        *url.URL
	AccessKeyID     string
	SecretAccessKey string
}

// New builds the client; it does not touch the network.
func New(ctx context.Context, o Options) (*Group, error) {
	if o.Name == "" {
		return nil, errors.New("burstaws: nome do grupo vazio")
	}
	load := []func(*awsconfig.LoadOptions) error{}
	if o.Region != "" {
		load = append(load, awsconfig.WithRegion(o.Region))
	}
	if o.AccessKeyID != "" {
		load = append(load, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(o.AccessKeyID, o.SecretAccessKey, "")))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, load...)
	if err != nil {
		return nil, fmt.Errorf("burstaws: configuração AWS: %w", err)
	}
	client := autoscaling.NewFromConfig(cfg, func(ao *autoscaling.Options) {
		if o.Endpoint != nil {
			ao.BaseEndpoint = aws.String(o.Endpoint.String())
		}
	})
	return &Group{API: client, Name: o.Name}, nil
}

// Describe implements burst.ASG.
func (g *Group) Describe(ctx context.Context) (burst.Group, error) {
	out, err := g.API.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{AutoScalingGroupNames: []string{g.Name}})
	if err != nil {
		return burst.Group{}, fmt.Errorf("burstaws: descrever %s: %w", g.Name, err)
	}
	if len(out.AutoScalingGroups) != 1 {
		return burst.Group{}, fmt.Errorf("burstaws: grupo %s não encontrado", g.Name)
	}
	asg := out.AutoScalingGroups[0]
	res := burst.Group{Desired: int(aws.ToInt32(asg.DesiredCapacity)), Max: int(aws.ToInt32(asg.MaxSize))}
	for _, in := range asg.Instances {
		res.Instances = append(res.Instances, burst.GroupInstance{
			ID: aws.ToString(in.InstanceId), State: string(in.LifecycleState), Protected: aws.ToBool(in.ProtectedFromScaleIn),
		})
	}
	return res, nil
}

// SetDesired implements burst.ASG. HonorCooldown is off: the scaler is the
// only policy of this group.
func (g *Group) SetDesired(ctx context.Context, n int) error {
	_, err := g.API.SetDesiredCapacity(ctx, &autoscaling.SetDesiredCapacityInput{
		AutoScalingGroupName: aws.String(g.Name), DesiredCapacity: aws.Int32(int32(max(min(n, 1000), 0))), HonorCooldown: aws.Bool(false), //nolint:gosec // bounded
	})
	if err != nil {
		return fmt.Errorf("burstaws: capacidade de %s para %d: %w", g.Name, n, err)
	}
	return nil
}

// Protect implements burst.ASG.
func (g *Group) Protect(ctx context.Context, ids []string, protected bool) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := g.API.SetInstanceProtection(ctx, &autoscaling.SetInstanceProtectionInput{
		AutoScalingGroupName: aws.String(g.Name), InstanceIds: ids, ProtectedFromScaleIn: aws.Bool(protected),
	})
	if err != nil {
		return fmt.Errorf("burstaws: proteção de %v em %s: %w", ids, g.Name, err)
	}
	return nil
}
