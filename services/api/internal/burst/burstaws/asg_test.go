package burstaws

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
)

type fakeAPI struct {
	groups   []types.AutoScalingGroup
	desired  *autoscaling.SetDesiredCapacityInput
	protect  *autoscaling.SetInstanceProtectionInput
	failWith error
}

func (f *fakeAPI) DescribeAutoScalingGroups(_ context.Context, in *autoscaling.DescribeAutoScalingGroupsInput, _ ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	if len(in.AutoScalingGroupNames) != 1 {
		return nil, errors.New("um grupo por vez")
	}
	return &autoscaling.DescribeAutoScalingGroupsOutput{AutoScalingGroups: f.groups}, nil
}

func (f *fakeAPI) SetDesiredCapacity(_ context.Context, in *autoscaling.SetDesiredCapacityInput, _ ...func(*autoscaling.Options)) (*autoscaling.SetDesiredCapacityOutput, error) {
	f.desired = in
	return &autoscaling.SetDesiredCapacityOutput{}, f.failWith
}

func (f *fakeAPI) SetInstanceProtection(_ context.Context, in *autoscaling.SetInstanceProtectionInput, _ ...func(*autoscaling.Options)) (*autoscaling.SetInstanceProtectionOutput, error) {
	f.protect = in
	return &autoscaling.SetInstanceProtectionOutput{}, f.failWith
}

func TestGroup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	api := &fakeAPI{groups: []types.AutoScalingGroup{{
		DesiredCapacity: aws.Int32(1), MaxSize: aws.Int32(2),
		Instances: []types.Instance{{InstanceId: aws.String("i-1"), LifecycleState: types.LifecycleStateInService, ProtectedFromScaleIn: aws.Bool(true)}},
	}}}
	g := &Group{API: api, Name: "docmatch-test-worker"}
	got, err := g.Describe(ctx)
	if err != nil || got.Desired != 1 || got.Max != 2 || len(got.Instances) != 1 || !got.Instances[0].Protected || got.Instances[0].State != "InService" {
		t.Fatalf("describe = %+v %v", got, err)
	}
	if err := g.SetDesired(ctx, 2); err != nil || aws.ToInt32(api.desired.DesiredCapacity) != 2 || aws.ToBool(api.desired.HonorCooldown) || aws.ToString(api.desired.AutoScalingGroupName) != g.Name {
		t.Fatalf("set desired = %+v %v", api.desired, err)
	}
	if err := g.Protect(ctx, nil, true); err != nil || api.protect != nil {
		t.Fatal("lista vazia não chama a AWS")
	}
	if err := g.Protect(ctx, []string{"i-1"}, false); err != nil || aws.ToBool(api.protect.ProtectedFromScaleIn) || api.protect.InstanceIds[0] != "i-1" {
		t.Fatalf("protect = %+v %v", api.protect, err)
	}
	api.groups = nil
	if _, err := g.Describe(ctx); err == nil {
		t.Fatal("grupo inexistente deveria falhar")
	}
	api.failWith = errors.New("AccessDenied")
	if _, err := g.Describe(ctx); err == nil {
		t.Fatal("erro da AWS deveria subir")
	}
	if err := g.SetDesired(ctx, 0); err == nil {
		t.Fatal("erro do set desired")
	}
	if err := g.Protect(ctx, []string{"i-1"}, true); err == nil {
		t.Fatal("erro do protect")
	}
}

// TestNewSpeaksTheAutoScalingAPI checks the real client against a local
// endpoint: the action names and the group name on the wire.
func TestNewSpeaksTheAutoScalingAPI(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var actions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		mu.Lock()
		actions = append(actions, form.Get("Action")+":"+form.Get("AutoScalingGroupName")+form.Get("AutoScalingGroupNames.member.1"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/xml")
		switch form.Get("Action") {
		case "DescribeAutoScalingGroups":
			_, _ = io.WriteString(w, `<DescribeAutoScalingGroupsResponse><DescribeAutoScalingGroupsResult><AutoScalingGroups><member><AutoScalingGroupName>g</AutoScalingGroupName><DesiredCapacity>0</DesiredCapacity><MaxSize>2</MaxSize><MinSize>0</MinSize><Instances/></member></AutoScalingGroups></DescribeAutoScalingGroupsResult></DescribeAutoScalingGroupsResponse>`)
		case "SetInstanceProtection":
			_, _ = io.WriteString(w, `<SetInstanceProtectionResponse><SetInstanceProtectionResult/><ResponseMetadata><RequestId>x</RequestId></ResponseMetadata></SetInstanceProtectionResponse>`)
		default:
			_, _ = io.WriteString(w, `<`+form.Get("Action")+`Response><ResponseMetadata><RequestId>x</RequestId></ResponseMetadata></`+form.Get("Action")+`Response>`)
		}
	}))
	t.Cleanup(srv.Close)
	endpoint, _ := url.Parse(srv.URL)
	if _, err := New(context.Background(), Options{}); err == nil {
		t.Fatal("sem nome deveria falhar")
	}
	g, err := New(context.Background(), Options{Name: "g", Region: "us-east-1", Endpoint: endpoint, AccessKeyID: "teste", SecretAccessKey: "teste"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := g.Describe(context.Background())
	if err != nil || got.Max != 2 {
		t.Fatalf("describe = %+v %v", got, err)
	}
	if err := g.SetDesired(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err := g.Protect(context.Background(), []string{"i-1"}, true); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(actions, ",") != "DescribeAutoScalingGroups:g,SetDesiredCapacity:g,SetInstanceProtection:g" {
		t.Fatalf("ações = %v", actions)
	}
}
