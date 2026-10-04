package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"zeros3-testing/profile/core"
)

// awsCLISmoke drives the AWS CLI through one short bucket/object/batch-delete
// cycle when `aws` is already installed; otherwise it reports one skip. It
// never installs anything.
func awsCLISmoke(cfg core.Config) core.Report {
	rep := core.Report{Profile: core.Profile, Client: "aws-cli", Endpoint: cfg.Endpoint}
	aws, err := exec.LookPath("aws")
	if err != nil {
		rep.Skipped = 1
		return rep
	}
	dir, err := os.MkdirTemp("", "zeros3-awscli-")
	if err != nil {
		rep.Failed, rep.Failures = 1, []core.Failure{{Scenario: "aws-cli", Contract: "scratch dir", Detail: err.Error()}}
		return rep
	}
	defer os.RemoveAll(dir)
	body := core.Data(1, 3<<20)
	src, dst := filepath.Join(dir, "src.bin"), filepath.Join(dir, "dst.bin")
	_ = os.WriteFile(src, body, 0o644)
	b := fmt.Sprintf("%s-cli", cfg.Prefix)
	env := append(os.Environ(), "AWS_ACCESS_KEY_ID="+cfg.AccessKey, "AWS_SECRET_ACCESS_KEY="+cfg.SecretKey,
		"AWS_DEFAULT_REGION="+cfg.Region, "AWS_EC2_METADATA_DISABLED=true", "AWS_PAGER=",
		"AWS_REQUEST_CHECKSUM_CALCULATION=when_required", "AWS_RESPONSE_CHECKSUM_VALIDATION=when_required") // the Core profile's checksum scope
	run := func(contract string, args ...string) string {
		cmd := exec.Command(aws, append([]string{"--endpoint-url", cfg.Endpoint}, args...)...)
		cmd.Env = env
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			rep.Failed++
			rep.Failures = append(rep.Failures, core.Failure{Scenario: "aws-cli", Contract: contract, Detail: err.Error() + ": " + out.String()})
			return ""
		}
		rep.Passed++
		return out.String()
	}
	run("CreateBucket", "s3api", "create-bucket", "--bucket", b)
	run("GetBucketLocation", "s3api", "get-bucket-location", "--bucket", b)
	run("PutObject (multipart above threshold)", "s3", "cp", "--only-show-errors", src, "s3://"+b+"/k/obj.bin")
	run("HeadObject", "s3api", "head-object", "--bucket", b, "--key", "k/obj.bin")
	run("GetObject", "s3", "cp", "--only-show-errors", "s3://"+b+"/k/obj.bin", dst)
	if got, _ := os.ReadFile(dst); bytes.Equal(got, body) {
		rep.Passed++
	} else {
		rep.Failed++
		rep.Failures = append(rep.Failures, core.Failure{Scenario: "aws-cli", Contract: "GetObject body is byte-exact", Detail: fmt.Sprintf("%d bytes", len(got))})
	}
	run("ListObjectsV2", "s3api", "list-objects-v2", "--bucket", b)
	run("DeleteObjects", "s3api", "delete-objects", "--bucket", b, "--delete", `{"Objects":[{"Key":"k/obj.bin"},{"Key":"absent"}]}`)
	run("DeleteBucket", "s3api", "delete-bucket", "--bucket", b)
	return rep
}
