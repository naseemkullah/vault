package vault

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fatih/structs"
	"github.com/go-test/deep"
	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/vault/audit"
	credUserpass "github.com/hashicorp/vault/builtin/credential/userpass"
	"github.com/hashicorp/vault/helper/builtinplugins"
	"github.com/hashicorp/vault/helper/identity"
	"github.com/hashicorp/vault/helper/namespace"
	"github.com/hashicorp/vault/helper/random"
	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/helper/compressutil"
	"github.com/hashicorp/vault/sdk/helper/consts"
	"github.com/hashicorp/vault/sdk/helper/jsonutil"
	"github.com/hashicorp/vault/sdk/helper/salt"
	"github.com/hashicorp/vault/sdk/logical"
	"github.com/hashicorp/vault/sdk/version"
	"github.com/mitchellh/mapstructure"
)

func TestSystemBackend_RootPaths(t *testing.T) {
	expected := []string{
		"auth/*",
		"remount",
		"audit",
		"audit/*",
		"raw",
		"raw/*",
		"replication/primary/secondary-token",
		"replication/performance/primary/secondary-token",
		"replication/dr/primary/secondary-token",
		"replication/reindex",
		"replication/dr/reindex",
		"replication/performance/reindex",
		"rotate",
		"config/cors",
		"config/auditing/*",
		"config/ui/headers/*",
		"plugins/catalog/*",
		"revoke-prefix/*",
		"revoke-force/*",
		"leases/revoke-prefix/*",
		"leases/revoke-force/*",
		"leases/lookup/*",
		"storage/raft/snapshot-auto/config/*",
		"leases",
	}

	b := testSystemBackend(t)
	actual := b.SpecialPaths().Root
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("bad: mismatch\nexpected:\n%#v\ngot:\n%#v", expected, actual)
	}
}

func TestSystemConfigCORS(t *testing.T) {
	b := testSystemBackend(t)
	_, barrier, _ := mockBarrier(t)
	view := NewBarrierView(barrier, "")
	b.(*SystemBackend).Core.systemBarrierView = view

	req := logical.TestRequest(t, logical.UpdateOperation, "config/cors")
	req.Data["allowed_origins"] = "http://www.example.com"
	req.Data["allowed_headers"] = "X-Custom-Header"
	_, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatal(err)
	}

	expected := &logical.Response{
		Data: map[string]interface{}{
			"enabled":         true,
			"allowed_origins": []string{"http://www.example.com"},
			"allowed_headers": append(StdAllowedHeaders, "X-Custom-Header"),
		},
	}

	req = logical.TestRequest(t, logical.ReadOperation, "config/cors")
	actual, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("bad: %#v", actual)
	}

	// Do it again. Bug #6182
	req = logical.TestRequest(t, logical.UpdateOperation, "config/cors")
	req.Data["allowed_origins"] = "http://www.example.com"
	req.Data["allowed_headers"] = "X-Custom-Header"
	_, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatal(err)
	}

	req = logical.TestRequest(t, logical.ReadOperation, "config/cors")
	actual, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("bad: %#v", actual)
	}

	req = logical.TestRequest(t, logical.DeleteOperation, "config/cors")
	_, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	req = logical.TestRequest(t, logical.ReadOperation, "config/cors")
	actual, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	expected = &logical.Response{
		Data: map[string]interface{}{
			"enabled": false,
		},
	}

	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("DELETE FAILED -- bad: %#v", actual)
	}
}

func TestSystemBackend_mounts(t *testing.T) {
	b := testSystemBackend(t)
	req := logical.TestRequest(t, logical.ReadOperation, "mounts")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	// We can't know the pointer address ahead of time so simply
	// copy what's given
	exp := map[string]interface{}{
		"secret/": map[string]interface{}{
			"type":                    "kv",
			"external_entropy_access": false,
			"description":             "key/value secret storage",
			"accessor":                resp.Data["secret/"].(map[string]interface{})["accessor"],
			"uuid":                    resp.Data["secret/"].(map[string]interface{})["uuid"],
			"config": map[string]interface{}{
				"default_lease_ttl": resp.Data["secret/"].(map[string]interface{})["config"].(map[string]interface{})["default_lease_ttl"].(int64),
				"max_lease_ttl":     resp.Data["secret/"].(map[string]interface{})["config"].(map[string]interface{})["max_lease_ttl"].(int64),
				"force_no_cache":    false,
			},
			"local":     false,
			"seal_wrap": false,
			"options": map[string]string{
				"version": "1",
			},
		},
		"sys/": map[string]interface{}{
			"type":                    "system",
			"external_entropy_access": false,
			"description":             "system endpoints used for control, policy and debugging",
			"accessor":                resp.Data["sys/"].(map[string]interface{})["accessor"],
			"uuid":                    resp.Data["sys/"].(map[string]interface{})["uuid"],
			"config": map[string]interface{}{
				"default_lease_ttl":           resp.Data["sys/"].(map[string]interface{})["config"].(map[string]interface{})["default_lease_ttl"].(int64),
				"max_lease_ttl":               resp.Data["sys/"].(map[string]interface{})["config"].(map[string]interface{})["max_lease_ttl"].(int64),
				"force_no_cache":              false,
				"passthrough_request_headers": []string{"Accept"},
			},
			"local":     false,
			"seal_wrap": true,
			"options":   map[string]string(nil),
		},
		"cubbyhole/": map[string]interface{}{
			"description":             "per-token private secret storage",
			"type":                    "cubbyhole",
			"external_entropy_access": false,
			"accessor":                resp.Data["cubbyhole/"].(map[string]interface{})["accessor"],
			"uuid":                    resp.Data["cubbyhole/"].(map[string]interface{})["uuid"],
			"config": map[string]interface{}{
				"default_lease_ttl": resp.Data["cubbyhole/"].(map[string]interface{})["config"].(map[string]interface{})["default_lease_ttl"].(int64),
				"max_lease_ttl":     resp.Data["cubbyhole/"].(map[string]interface{})["config"].(map[string]interface{})["max_lease_ttl"].(int64),
				"force_no_cache":    false,
			},
			"local":     true,
			"seal_wrap": false,
			"options":   map[string]string(nil),
		},
		"identity/": map[string]interface{}{
			"description":             "identity store",
			"type":                    "identity",
			"external_entropy_access": false,
			"accessor":                resp.Data["identity/"].(map[string]interface{})["accessor"],
			"uuid":                    resp.Data["identity/"].(map[string]interface{})["uuid"],
			"config": map[string]interface{}{
				"default_lease_ttl":           resp.Data["identity/"].(map[string]interface{})["config"].(map[string]interface{})["default_lease_ttl"].(int64),
				"max_lease_ttl":               resp.Data["identity/"].(map[string]interface{})["config"].(map[string]interface{})["max_lease_ttl"].(int64),
				"force_no_cache":              false,
				"passthrough_request_headers": []string{"Authorization"},
			},
			"local":     false,
			"seal_wrap": false,
			"options":   map[string]string(nil),
		},
	}
	if diff := deep.Equal(resp.Data, exp); len(diff) > 0 {
		t.Fatalf("bad, diff: %#v", diff)
	}

	for name, conf := range exp {
		req := logical.TestRequest(t, logical.ReadOperation, "mounts/"+name)
		resp, err = b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if diff := deep.Equal(resp.Data, conf); len(diff) > 0 {
			t.Fatalf("bad, diff: %#v", diff)
		}
	}
}

func TestSystemBackend_mount(t *testing.T) {
	b := testSystemBackend(t)

	req := logical.TestRequest(t, logical.UpdateOperation, "mounts/prod/secret/")
	req.Data["type"] = "kv"
	req.Data["config"] = map[string]interface{}{
		"default_lease_ttl": "35m",
		"max_lease_ttl":     "45m",
	}
	req.Data["local"] = true
	req.Data["seal_wrap"] = true
	req.Data["options"] = map[string]string{
		"version": "1",
	}

	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %v", resp)
	}

	req = logical.TestRequest(t, logical.ReadOperation, "mounts")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	// We can't know the pointer address ahead of time so simply
	// copy what's given
	exp := map[string]interface{}{
		"secret/": map[string]interface{}{
			"type":                    "kv",
			"external_entropy_access": false,
			"description":             "key/value secret storage",
			"accessor":                resp.Data["secret/"].(map[string]interface{})["accessor"],
			"uuid":                    resp.Data["secret/"].(map[string]interface{})["uuid"],
			"config": map[string]interface{}{
				"default_lease_ttl": resp.Data["secret/"].(map[string]interface{})["config"].(map[string]interface{})["default_lease_ttl"].(int64),
				"max_lease_ttl":     resp.Data["secret/"].(map[string]interface{})["config"].(map[string]interface{})["max_lease_ttl"].(int64),
				"force_no_cache":    false,
			},
			"local":     false,
			"seal_wrap": false,
			"options": map[string]string{
				"version": "1",
			},
		},
		"sys/": map[string]interface{}{
			"type":                    "system",
			"external_entropy_access": false,
			"description":             "system endpoints used for control, policy and debugging",
			"accessor":                resp.Data["sys/"].(map[string]interface{})["accessor"],
			"uuid":                    resp.Data["sys/"].(map[string]interface{})["uuid"],
			"config": map[string]interface{}{
				"default_lease_ttl":           resp.Data["sys/"].(map[string]interface{})["config"].(map[string]interface{})["default_lease_ttl"].(int64),
				"max_lease_ttl":               resp.Data["sys/"].(map[string]interface{})["config"].(map[string]interface{})["max_lease_ttl"].(int64),
				"force_no_cache":              false,
				"passthrough_request_headers": []string{"Accept"},
			},
			"local":     false,
			"seal_wrap": true,
			"options":   map[string]string(nil),
		},
		"cubbyhole/": map[string]interface{}{
			"description":             "per-token private secret storage",
			"type":                    "cubbyhole",
			"external_entropy_access": false,
			"accessor":                resp.Data["cubbyhole/"].(map[string]interface{})["accessor"],
			"uuid":                    resp.Data["cubbyhole/"].(map[string]interface{})["uuid"],
			"config": map[string]interface{}{
				"default_lease_ttl": resp.Data["cubbyhole/"].(map[string]interface{})["config"].(map[string]interface{})["default_lease_ttl"].(int64),
				"max_lease_ttl":     resp.Data["cubbyhole/"].(map[string]interface{})["config"].(map[string]interface{})["max_lease_ttl"].(int64),
				"force_no_cache":    false,
			},
			"local":     true,
			"seal_wrap": false,
			"options":   map[string]string(nil),
		},
		"identity/": map[string]interface{}{
			"description":             "identity store",
			"type":                    "identity",
			"external_entropy_access": false,
			"accessor":                resp.Data["identity/"].(map[string]interface{})["accessor"],
			"uuid":                    resp.Data["identity/"].(map[string]interface{})["uuid"],
			"config": map[string]interface{}{
				"default_lease_ttl":           resp.Data["identity/"].(map[string]interface{})["config"].(map[string]interface{})["default_lease_ttl"].(int64),
				"max_lease_ttl":               resp.Data["identity/"].(map[string]interface{})["config"].(map[string]interface{})["max_lease_ttl"].(int64),
				"force_no_cache":              false,
				"passthrough_request_headers": []string{"Authorization"},
			},
			"local":     false,
			"seal_wrap": false,
			"options":   map[string]string(nil),
		},
		"prod/secret/": map[string]interface{}{
			"description":             "",
			"type":                    "kv",
			"external_entropy_access": false,
			"accessor":                resp.Data["prod/secret/"].(map[string]interface{})["accessor"],
			"uuid":                    resp.Data["prod/secret/"].(map[string]interface{})["uuid"],
			"config": map[string]interface{}{
				"default_lease_ttl": int64(2100),
				"max_lease_ttl":     int64(2700),
				"force_no_cache":    false,
			},
			"local":     true,
			"seal_wrap": true,
			"options": map[string]string{
				"version": "1",
			},
		},
	}
	if diff := deep.Equal(resp.Data, exp); len(diff) > 0 {
		t.Fatalf("bad: diff: %#v", diff)
	}
}

func TestSystemBackend_mount_force_no_cache(t *testing.T) {
	core, b, _ := testCoreSystemBackend(t)

	req := logical.TestRequest(t, logical.UpdateOperation, "mounts/prod/secret/")
	req.Data["type"] = "kv"
	req.Data["config"] = map[string]interface{}{
		"force_no_cache": true,
	}

	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %v", resp)
	}

	mountEntry := core.router.MatchingMountEntry(namespace.RootContext(nil), "prod/secret/")
	if mountEntry == nil {
		t.Fatalf("missing mount entry")
	}
	if !mountEntry.Config.ForceNoCache {
		t.Fatalf("bad config %#v", mountEntry)
	}
}

func TestSystemBackend_mount_invalid(t *testing.T) {
	b := testSystemBackend(t)

	req := logical.TestRequest(t, logical.UpdateOperation, "mounts/prod/secret/")
	req.Data["type"] = "nope"
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if resp.Data["error"] != `plugin not found in the catalog: nope` {
		t.Fatalf("bad: %v", resp)
	}
}

func TestSystemBackend_unmount(t *testing.T) {
	b := testSystemBackend(t)

	req := logical.TestRequest(t, logical.DeleteOperation, "mounts/secret/")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %v", resp)
	}
}

var capabilitiesPolicy = `
name = "test"
path "foo/bar*" {
	capabilities = ["create", "sudo", "update"]
}
path "sys/capabilities*" {
	capabilities = ["update"]
}
path "bar/baz" {
	capabilities = ["read", "update"]
}
path "bar/baz" {
	capabilities = ["delete"]
}
`

func TestSystemBackend_PathCapabilities(t *testing.T) {
	var resp *logical.Response
	var err error

	core, b, rootToken := testCoreSystemBackend(t)

	policy, _ := ParseACLPolicy(namespace.RootNamespace, capabilitiesPolicy)
	err = core.policyStore.SetPolicy(namespace.RootContext(nil), policy)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	path1 := "foo/bar"
	path2 := "foo/bar/sample"
	path3 := "sys/capabilities"
	path4 := "bar/baz"

	rootCheckFunc := func(t *testing.T, resp *logical.Response) {
		// All the paths should have "root" as the capability
		expectedRoot := []string{"root"}
		if !reflect.DeepEqual(resp.Data[path1], expectedRoot) ||
			!reflect.DeepEqual(resp.Data[path2], expectedRoot) ||
			!reflect.DeepEqual(resp.Data[path3], expectedRoot) ||
			!reflect.DeepEqual(resp.Data[path4], expectedRoot) {
			t.Fatalf("bad: capabilities; expected: %#v, actual: %#v", expectedRoot, resp.Data)
		}
	}

	// Check the capabilities using the root token
	resp, err = b.HandleRequest(namespace.RootContext(nil), &logical.Request{
		Path:      "capabilities",
		Operation: logical.UpdateOperation,
		Data: map[string]interface{}{
			"paths": []string{path1, path2, path3, path4},
			"token": rootToken,
		},
	})
	if err != nil || (resp != nil && resp.IsError()) {
		t.Fatalf("bad: resp: %#v\nerr: %v", resp, err)
	}
	rootCheckFunc(t, resp)

	// Check the capabilities using capabilities-self
	resp, err = b.HandleRequest(namespace.RootContext(nil), &logical.Request{
		ClientToken: rootToken,
		Path:        "capabilities-self",
		Operation:   logical.UpdateOperation,
		Data: map[string]interface{}{
			"paths": []string{path1, path2, path3, path4},
		},
	})
	if err != nil || (resp != nil && resp.IsError()) {
		t.Fatalf("bad: resp: %#v\nerr: %v", resp, err)
	}
	rootCheckFunc(t, resp)

	// Lookup the accessor of the root token
	te, err := core.tokenStore.Lookup(namespace.RootContext(nil), rootToken)
	if err != nil {
		t.Fatal(err)
	}

	// Check the capabilities using capabilities-accessor endpoint
	resp, err = b.HandleRequest(namespace.RootContext(nil), &logical.Request{
		Path:      "capabilities-accessor",
		Operation: logical.UpdateOperation,
		Data: map[string]interface{}{
			"paths":    []string{path1, path2, path3, path4},
			"accessor": te.Accessor,
		},
	})
	if err != nil || (resp != nil && resp.IsError()) {
		t.Fatalf("bad: resp: %#v\nerr: %v", resp, err)
	}
	rootCheckFunc(t, resp)

	// Create a non-root token
	testMakeServiceTokenViaBackend(t, core.tokenStore, rootToken, "tokenid", "", []string{"test"})

	nonRootCheckFunc := func(t *testing.T, resp *logical.Response) {
		expected1 := []string{"create", "sudo", "update"}
		expected2 := expected1
		expected3 := []string{"update"}
		expected4 := []string{"delete", "read", "update"}

		if !reflect.DeepEqual(resp.Data[path1], expected1) ||
			!reflect.DeepEqual(resp.Data[path2], expected2) ||
			!reflect.DeepEqual(resp.Data[path3], expected3) ||
			!reflect.DeepEqual(resp.Data[path4], expected4) {
			t.Fatalf("bad: capabilities; actual: %#v", resp.Data)
		}
	}

	// Check the capabilities using a non-root token
	resp, err = b.HandleRequest(namespace.RootContext(nil), &logical.Request{
		Path:      "capabilities",
		Operation: logical.UpdateOperation,
		Data: map[string]interface{}{
			"paths": []string{path1, path2, path3, path4},
			"token": "tokenid",
		},
	})
	if err != nil || (resp != nil && resp.IsError()) {
		t.Fatalf("bad: resp: %#v\nerr: %v", resp, err)
	}
	nonRootCheckFunc(t, resp)

	// Check the capabilities of a non-root token using capabilities-self
	// endpoint
	resp, err = b.HandleRequest(namespace.RootContext(nil), &logical.Request{
		ClientToken: "tokenid",
		Path:        "capabilities-self",
		Operation:   logical.UpdateOperation,
		Data: map[string]interface{}{
			"paths": []string{path1, path2, path3, path4},
		},
	})
	if err != nil || (resp != nil && resp.IsError()) {
		t.Fatalf("bad: resp: %#v\nerr: %v", resp, err)
	}
	nonRootCheckFunc(t, resp)

	// Lookup the accessor of the non-root token
	te, err = core.tokenStore.Lookup(namespace.RootContext(nil), "tokenid")
	if err != nil {
		t.Fatal(err)
	}

	// Check the capabilities using a non-root token using
	// capabilities-accessor endpoint
	resp, err = b.HandleRequest(namespace.RootContext(nil), &logical.Request{
		Path:      "capabilities-accessor",
		Operation: logical.UpdateOperation,
		Data: map[string]interface{}{
			"paths":    []string{path1, path2, path3, path4},
			"accessor": te.Accessor,
		},
	})
	if err != nil || (resp != nil && resp.IsError()) {
		t.Fatalf("bad: resp: %#v\nerr: %v", resp, err)
	}
	nonRootCheckFunc(t, resp)
}

func TestSystemBackend_Capabilities_BC(t *testing.T) {
	testCapabilities(t, "capabilities")
	testCapabilities(t, "capabilities-self")
}

func testCapabilities(t *testing.T, endpoint string) {
	core, b, rootToken := testCoreSystemBackend(t)
	req := logical.TestRequest(t, logical.UpdateOperation, endpoint)
	if endpoint == "capabilities-self" {
		req.ClientToken = rootToken
	} else {
		req.Data["token"] = rootToken
	}
	req.Data["path"] = "any_path"

	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatalf("bad: %v", resp)
	}

	actual := resp.Data["capabilities"]
	expected := []string{"root"}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("bad: got\n%#v\nexpected\n%#v\n", actual, expected)
	}

	policy, _ := ParseACLPolicy(namespace.RootNamespace, capabilitiesPolicy)
	err = core.policyStore.SetPolicy(namespace.RootContext(nil), policy)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	testMakeServiceTokenViaBackend(t, core.tokenStore, rootToken, "tokenid", "", []string{"test"})
	req = logical.TestRequest(t, logical.UpdateOperation, endpoint)
	if endpoint == "capabilities-self" {
		req.ClientToken = "tokenid"
	} else {
		req.Data["token"] = "tokenid"
	}
	req.Data["path"] = "foo/bar"

	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil {
		t.Fatalf("bad: %v", resp)
	}

	actual = resp.Data["capabilities"]
	expected = []string{"create", "sudo", "update"}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("bad: got\n%#v\nexpected\n%#v\n", actual, expected)
	}
}

func TestSystemBackend_CapabilitiesAccessor_BC(t *testing.T) {
	core, b, rootToken := testCoreSystemBackend(t)
	te, err := core.tokenStore.Lookup(namespace.RootContext(nil), rootToken)
	if err != nil {
		t.Fatal(err)
	}

	req := logical.TestRequest(t, logical.UpdateOperation, "capabilities-accessor")
	// Accessor of root token
	req.Data["accessor"] = te.Accessor
	req.Data["path"] = "any_path"

	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil {
		t.Fatalf("bad: %v", resp)
	}

	actual := resp.Data["capabilities"]
	expected := []string{"root"}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("bad: got\n%#v\nexpected\n%#v\n", actual, expected)
	}

	policy, _ := ParseACLPolicy(namespace.RootNamespace, capabilitiesPolicy)
	err = core.policyStore.SetPolicy(namespace.RootContext(nil), policy)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	testMakeServiceTokenViaBackend(t, core.tokenStore, rootToken, "tokenid", "", []string{"test"})

	te, err = core.tokenStore.Lookup(namespace.RootContext(nil), "tokenid")
	if err != nil {
		t.Fatal(err)
	}

	req = logical.TestRequest(t, logical.UpdateOperation, "capabilities-accessor")
	req.Data["accessor"] = te.Accessor
	req.Data["path"] = "foo/bar"

	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil {
		t.Fatalf("bad: %v", resp)
	}

	actual = resp.Data["capabilities"]
	expected = []string{"create", "sudo", "update"}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("bad: got\n%#v\nexpected\n%#v\n", actual, expected)
	}
}

func TestSystemBackend_remount_auth(t *testing.T) {
	err := AddTestCredentialBackend("userpass", credUserpass.Factory)
	if err != nil {
		t.Fatal(err)
	}

	c, b, _ := testCoreSystemBackend(t)

	userpassMe := &MountEntry{
		Table:       credentialTableType,
		Path:        "userpass1/",
		Type:        "userpass",
		Description: "userpass",
	}
	err = c.enableCredential(namespace.RootContext(nil), userpassMe)
	if err != nil {
		t.Fatal(err)
	}

	req := logical.TestRequest(t, logical.UpdateOperation, "remount")
	req.Data["from"] = "auth/userpass1"
	req.Data["to"] = "auth/userpass2"
	req.Data["config"] = structs.Map(MountConfig{})
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)

	RetryUntil(t, 5*time.Second, func() error {
		req = logical.TestRequest(t, logical.ReadOperation, fmt.Sprintf("remount/status/%s", resp.Data["migration_id"]))
		resp, err = b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		migrationInfo := resp.Data["migration_info"].(*MountMigrationInfo)
		if migrationInfo.MigrationStatus != MigrationSuccessStatus.String() {
			return fmt.Errorf("Expected migration status to be successful, got %q", migrationInfo.MigrationStatus)
		}
		return nil
	})
}

func TestSystemBackend_remount_auth_invalid(t *testing.T) {
	b := testSystemBackend(t)

	req := logical.TestRequest(t, logical.UpdateOperation, "remount")
	req.Data["from"] = "auth/unknown"
	req.Data["to"] = "auth/foo"
	req.Data["config"] = structs.Map(MountConfig{})
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)

	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(resp.Data["error"].(string), "no matching mount at \"auth/unknown/\"") {
		t.Fatalf("Found unexpected error %q", resp.Data["error"].(string))
	}

	req.Data["to"] = "foo"
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)

	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(resp.Data["error"].(string), "cannot remount auth mount to non-auth mount \"foo/\"") {
		t.Fatalf("Found unexpected error %q", resp.Data["error"].(string))
	}
}

func TestSystemBackend_remount_auth_protected(t *testing.T) {
	b := testSystemBackend(t)

	req := logical.TestRequest(t, logical.UpdateOperation, "remount")
	req.Data["from"] = "auth/token"
	req.Data["to"] = "auth/foo"
	req.Data["config"] = structs.Map(MountConfig{})
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)

	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(resp.Data["error"].(string), "cannot remount \"auth/token/\"") {
		t.Fatalf("Found unexpected error %q", resp.Data["error"].(string))
	}

	req.Data["from"] = "auth/foo"
	req.Data["to"] = "auth/token"
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)

	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(resp.Data["error"].(string), "cannot remount to destination \"auth/token/\"") {
		t.Fatalf("Found unexpected error %q", resp.Data["error"].(string))
	}
}

func TestSystemBackend_remount_auth_destinationInUse(t *testing.T) {
	err := AddTestCredentialBackend("userpass", credUserpass.Factory)
	if err != nil {
		t.Fatal(err)
	}

	c, b, _ := testCoreSystemBackend(t)

	userpassMe := &MountEntry{
		Table:       credentialTableType,
		Path:        "userpass1/",
		Type:        "userpass",
		Description: "userpass",
	}
	err = c.enableCredential(namespace.RootContext(nil), userpassMe)
	if err != nil {
		t.Fatal(err)
	}

	userpassMe2 := &MountEntry{
		Table:       credentialTableType,
		Path:        "userpass2/",
		Type:        "userpass",
		Description: "userpass",
	}
	err = c.enableCredential(namespace.RootContext(nil), userpassMe2)
	if err != nil {
		t.Fatal(err)
	}

	req := logical.TestRequest(t, logical.UpdateOperation, "remount")
	req.Data["from"] = "auth/userpass1"
	req.Data["to"] = "auth/userpass2"
	req.Data["config"] = structs.Map(MountConfig{})
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)

	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(resp.Data["error"].(string), "path already in use at \"auth/userpass2/\"") {
		t.Fatalf("Found unexpected error %q", resp.Data["error"].(string))
	}

	req.Data["to"] = "auth/userpass2/mypass"
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)

	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(resp.Data["error"].(string), "path already in use at \"auth/userpass2/\"") {
		t.Fatalf("Found unexpected error %q", resp.Data["error"].(string))
	}

	userpassMe3 := &MountEntry{
		Table:       credentialTableType,
		Path:        "userpass3/mypass/",
		Type:        "userpass",
		Description: "userpass",
	}
	err = c.enableCredential(namespace.RootContext(nil), userpassMe3)
	if err != nil {
		t.Fatal(err)
	}

	req.Data["to"] = "auth/userpass3/"
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)

	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(resp.Data["error"].(string), "path already in use at \"auth/userpass3/mypass/\"") {
		t.Fatalf("Found unexpected error %q", resp.Data["error"].(string))
	}
}

func TestSystemBackend_remount(t *testing.T) {
	b := testSystemBackend(t)

	req := logical.TestRequest(t, logical.UpdateOperation, "remount")
	req.Data["from"] = "secret"
	req.Data["to"] = "foo"
	req.Data["config"] = structs.Map(MountConfig{})
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	RetryUntil(t, 5*time.Second, func() error {
		req = logical.TestRequest(t, logical.ReadOperation, fmt.Sprintf("remount/status/%s", resp.Data["migration_id"]))
		resp, err = b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		migrationInfo := resp.Data["migration_info"].(*MountMigrationInfo)
		if migrationInfo.MigrationStatus != MigrationSuccessStatus.String() {
			return fmt.Errorf("Expected migration status to be successful, got %q", migrationInfo.MigrationStatus)
		}
		return nil
	})
}

func TestSystemBackend_remount_invalid(t *testing.T) {
	b := testSystemBackend(t)

	req := logical.TestRequest(t, logical.UpdateOperation, "remount")
	req.Data["from"] = "unknown"
	req.Data["to"] = "foo"
	req.Data["config"] = structs.Map(MountConfig{})
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(resp.Data["error"].(string), "no matching mount at \"unknown/\"") {
		t.Fatalf("Found unexpected error %q", resp.Data["error"].(string))
	}
}

func TestSystemBackend_remount_system(t *testing.T) {
	b := testSystemBackend(t)

	req := logical.TestRequest(t, logical.UpdateOperation, "remount")
	req.Data["from"] = "sys"
	req.Data["to"] = "foo"
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(resp.Data["error"].(string), "cannot remount \"sys/\"") {
		t.Fatalf("Found unexpected error %q", resp.Data["error"].(string))
	}
}

func TestSystemBackend_remount_clean(t *testing.T) {
	b := testSystemBackend(t)

	req := logical.TestRequest(t, logical.UpdateOperation, "remount")
	req.Data["from"] = "foo"
	req.Data["to"] = "foo//bar"
	req.Data["config"] = structs.Map(MountConfig{})
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if resp.Data["error"] != `invalid destination mount: path 'foo//bar/' does not match cleaned path 'foo/bar/'` {
		t.Fatalf("bad: %v", resp)
	}
}

func TestSystemBackend_remount_nonPrintable(t *testing.T) {
	b := testSystemBackend(t)

	req := logical.TestRequest(t, logical.UpdateOperation, "remount")
	req.Data["from"] = "foo"
	req.Data["to"] = "foo\nbar"
	req.Data["config"] = structs.Map(MountConfig{})
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if resp.Data["error"] != `invalid destination mount: path cannot contain non-printable characters` {
		t.Fatalf("bad: %v", resp)
	}
}

func TestSystemBackend_leases(t *testing.T) {
	core, b, root := testCoreSystemBackend(t)

	// Create a key with a lease
	req := logical.TestRequest(t, logical.UpdateOperation, "secret/foo")
	req.Data["foo"] = "bar"
	req.ClientToken = root
	resp, err := core.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %#v", resp)
	}

	// Read a key with a LeaseID
	req = logical.TestRequest(t, logical.ReadOperation, "secret/foo")
	req.ClientToken = root
	req.SetTokenEntry(&logical.TokenEntry{ID: root, NamespaceID: "root", Policies: []string{"root"}})
	resp, err = core.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil || resp.Secret == nil || resp.Secret.LeaseID == "" {
		t.Fatalf("bad: %#v", resp)
	}

	// Read lease
	req = logical.TestRequest(t, logical.UpdateOperation, "leases/lookup")
	req.Data["lease_id"] = resp.Secret.LeaseID
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Data["renewable"] == nil || resp.Data["renewable"].(bool) {
		t.Fatal("kv leases are not renewable")
	}

	// Invalid lease
	req = logical.TestRequest(t, logical.UpdateOperation, "leases/lookup")
	req.Data["lease_id"] = "invalid"
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != logical.ErrInvalidRequest {
		t.Fatalf("expected invalid request, got err: %v", err)
	}
}

func TestSystemBackend_leases_list(t *testing.T) {
	core, b, root := testCoreSystemBackend(t)

	// Create a key with a lease
	req := logical.TestRequest(t, logical.UpdateOperation, "secret/foo")
	req.Data["foo"] = "bar"
	req.ClientToken = root
	resp, err := core.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %#v", resp)
	}

	// Read a key with a LeaseID
	req = logical.TestRequest(t, logical.ReadOperation, "secret/foo")
	req.ClientToken = root
	req.SetTokenEntry(&logical.TokenEntry{ID: root, NamespaceID: "root", Policies: []string{"root"}})
	resp, err = core.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil || resp.Secret == nil || resp.Secret.LeaseID == "" {
		t.Fatalf("bad: %#v", resp)
	}

	// List top level
	req = logical.TestRequest(t, logical.ListOperation, "leases/lookup/")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil || resp.Data == nil {
		t.Fatalf("bad: %#v", resp)
	}
	var keys []string
	if err := mapstructure.WeakDecode(resp.Data["keys"], &keys); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("Expected 1 subkey lease, got %d: %#v", len(keys), keys)
	}
	if keys[0] != "secret/" {
		t.Fatal("Expected only secret subkey")
	}

	// List lease
	req = logical.TestRequest(t, logical.ListOperation, "leases/lookup/secret/foo")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil || resp.Data == nil {
		t.Fatalf("bad: %#v", resp)
	}
	keys = []string{}
	if err := mapstructure.WeakDecode(resp.Data["keys"], &keys); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("Expected 1 secret lease, got %d: %#v", len(keys), keys)
	}

	// Generate multiple leases
	req = logical.TestRequest(t, logical.ReadOperation, "secret/foo")
	req.ClientToken = root
	req.SetTokenEntry(&logical.TokenEntry{ID: root, NamespaceID: "root", Policies: []string{"root"}})
	resp, err = core.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil || resp.Secret == nil || resp.Secret.LeaseID == "" {
		t.Fatalf("bad: %#v", resp)
	}

	req = logical.TestRequest(t, logical.ReadOperation, "secret/foo")
	req.ClientToken = root
	req.SetTokenEntry(&logical.TokenEntry{ID: root, NamespaceID: "root", Policies: []string{"root"}})
	resp, err = core.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil || resp.Secret == nil || resp.Secret.LeaseID == "" {
		t.Fatalf("bad: %#v", resp)
	}

	req = logical.TestRequest(t, logical.ListOperation, "leases/lookup/secret/foo")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil || resp.Data == nil {
		t.Fatalf("bad: %#v", resp)
	}
	keys = []string{}
	if err := mapstructure.WeakDecode(resp.Data["keys"], &keys); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("Expected 3 secret lease, got %d: %#v", len(keys), keys)
	}

	// Listing subkeys
	req = logical.TestRequest(t, logical.UpdateOperation, "secret/bar")
	req.Data["foo"] = "bar"
	req.ClientToken = root
	resp, err = core.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %#v", resp)
	}

	// Read a key with a LeaseID
	req = logical.TestRequest(t, logical.ReadOperation, "secret/bar")
	req.ClientToken = root
	req.SetTokenEntry(&logical.TokenEntry{ID: root, NamespaceID: "root", Policies: []string{"root"}})
	resp, err = core.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil || resp.Secret == nil || resp.Secret.LeaseID == "" {
		t.Fatalf("bad: %#v", resp)
	}

	req = logical.TestRequest(t, logical.ListOperation, "leases/lookup/secret")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil || resp.Data == nil {
		t.Fatalf("bad: %#v", resp)
	}
	keys = []string{}
	if err := mapstructure.WeakDecode(resp.Data["keys"], &keys); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("Expected 2 secret lease, got %d: %#v", len(keys), keys)
	}
	expected := []string{"bar/", "foo/"}
	if !reflect.DeepEqual(expected, keys) {
		t.Fatalf("exp: %#v, act: %#v", expected, keys)
	}
}

func TestSystemBackend_renew(t *testing.T) {
	core, b, root := testCoreSystemBackend(t)

	// Create a key with a lease
	req := logical.TestRequest(t, logical.UpdateOperation, "secret/foo")
	req.Data["foo"] = "bar"
	req.ClientToken = root
	resp, err := core.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %#v", resp)
	}

	// Read a key with a LeaseID
	req = logical.TestRequest(t, logical.ReadOperation, "secret/foo")
	req.ClientToken = root
	req.SetTokenEntry(&logical.TokenEntry{ID: root, NamespaceID: "root", Policies: []string{"root"}})
	resp, err = core.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil || resp.Secret == nil || resp.Secret.LeaseID == "" {
		t.Fatalf("bad: %#v", resp)
	}

	// Attempt renew
	req2 := logical.TestRequest(t, logical.UpdateOperation, "leases/renew/"+resp.Secret.LeaseID)
	resp2, err := b.HandleRequest(namespace.RootContext(nil), req2)
	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}

	// Should get error about non-renewability
	if resp2.Data["error"] != "lease is not renewable" {
		t.Fatalf("bad: %#v", resp)
	}

	// Add a TTL to the lease
	req = logical.TestRequest(t, logical.UpdateOperation, "secret/foo")
	req.Data["foo"] = "bar"
	req.Data["ttl"] = "180s"
	req.ClientToken = root
	resp, err = core.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %#v", resp)
	}

	// Read a key with a LeaseID
	req = logical.TestRequest(t, logical.ReadOperation, "secret/foo")
	req.ClientToken = root
	req.SetTokenEntry(&logical.TokenEntry{ID: root, NamespaceID: "root", Policies: []string{"root"}})
	resp, err = core.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil || resp.Secret == nil || resp.Secret.LeaseID == "" {
		t.Fatalf("bad: %#v", resp)
	}

	// Attempt renew
	req2 = logical.TestRequest(t, logical.UpdateOperation, "leases/renew/"+resp.Secret.LeaseID)
	resp2, err = b.HandleRequest(namespace.RootContext(nil), req2)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp2.IsError() {
		t.Fatalf("got an error")
	}
	if resp2.Data == nil {
		t.Fatal("nil data")
	}
	if resp.Secret.TTL != 180*time.Second {
		t.Fatalf("bad lease duration: %v", resp.Secret.TTL)
	}

	// Test the other route path
	req2 = logical.TestRequest(t, logical.UpdateOperation, "leases/renew")
	req2.Data["lease_id"] = resp.Secret.LeaseID
	resp2, err = b.HandleRequest(namespace.RootContext(nil), req2)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp2.IsError() {
		t.Fatalf("got an error")
	}
	if resp2.Data == nil {
		t.Fatal("nil data")
	}
	if resp.Secret.TTL != 180*time.Second {
		t.Fatalf("bad lease duration: %v", resp.Secret.TTL)
	}

	// Test orig path
	req2 = logical.TestRequest(t, logical.UpdateOperation, "renew")
	req2.Data["lease_id"] = resp.Secret.LeaseID
	resp2, err = b.HandleRequest(namespace.RootContext(nil), req2)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp2.IsError() {
		t.Fatalf("got an error")
	}
	if resp2.Data == nil {
		t.Fatal("nil data")
	}
	if resp.Secret.TTL != time.Second*180 {
		t.Fatalf("bad lease duration: %v", resp.Secret.TTL)
	}
}

func TestSystemBackend_renew_invalidID(t *testing.T) {
	b := testSystemBackend(t)

	// Attempt renew
	req := logical.TestRequest(t, logical.UpdateOperation, "leases/renew/foobarbaz")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if resp.Data["error"] != "lease not found" {
		t.Fatalf("bad: %v", resp)
	}

	// Attempt renew with other method
	req = logical.TestRequest(t, logical.UpdateOperation, "leases/renew")
	req.Data["lease_id"] = "foobarbaz"
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if resp.Data["error"] != "lease not found" {
		t.Fatalf("bad: %v", resp)
	}
}

func TestSystemBackend_renew_invalidID_origUrl(t *testing.T) {
	b := testSystemBackend(t)

	// Attempt renew
	req := logical.TestRequest(t, logical.UpdateOperation, "renew/foobarbaz")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if resp.Data["error"] != "lease not found" {
		t.Fatalf("bad: %v", resp)
	}

	// Attempt renew with other method
	req = logical.TestRequest(t, logical.UpdateOperation, "renew")
	req.Data["lease_id"] = "foobarbaz"
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if resp.Data["error"] != "lease not found" {
		t.Fatalf("bad: %v", resp)
	}
}

func TestSystemBackend_revoke(t *testing.T) {
	core, b, root := testCoreSystemBackend(t)

	// Create a key with a lease
	req := logical.TestRequest(t, logical.UpdateOperation, "secret/foo")
	req.Data["foo"] = "bar"
	req.Data["lease"] = "1h"
	req.ClientToken = root
	resp, err := core.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %#v", resp)
	}

	// Read a key with a LeaseID
	req = logical.TestRequest(t, logical.ReadOperation, "secret/foo")
	req.ClientToken = root
	req.SetTokenEntry(&logical.TokenEntry{ID: root, NamespaceID: "root", Policies: []string{"root"}})
	resp, err = core.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil || resp.Secret == nil || resp.Secret.LeaseID == "" {
		t.Fatalf("bad: %#v", resp)
	}

	// Attempt revoke
	req2 := logical.TestRequest(t, logical.UpdateOperation, "revoke/"+resp.Secret.LeaseID)
	resp2, err := b.HandleRequest(namespace.RootContext(nil), req2)
	if err != nil {
		t.Fatalf("err: %v %#v", err, resp2)
	}
	if resp2 != nil {
		t.Fatalf("bad: %#v", resp)
	}

	// Attempt renew
	req3 := logical.TestRequest(t, logical.UpdateOperation, "renew/"+resp.Secret.LeaseID)
	resp3, err := b.HandleRequest(namespace.RootContext(nil), req3)
	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if resp3.Data["error"] != "lease not found" {
		t.Fatalf("bad: %v", *resp3)
	}

	// Read a key with a LeaseID
	req = logical.TestRequest(t, logical.ReadOperation, "secret/foo")
	req.ClientToken = root
	req.SetTokenEntry(&logical.TokenEntry{ID: root, NamespaceID: "root", Policies: []string{"root"}})
	resp, err = core.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil || resp.Secret == nil || resp.Secret.LeaseID == "" {
		t.Fatalf("bad: %#v", resp)
	}

	// Test the other route path
	req2 = logical.TestRequest(t, logical.UpdateOperation, "revoke")
	req2.Data["lease_id"] = resp.Secret.LeaseID
	resp2, err = b.HandleRequest(namespace.RootContext(nil), req2)
	if err != nil {
		t.Fatalf("err: %v %#v", err, resp2)
	}
	if resp2 != nil {
		t.Fatalf("bad: %#v", resp)
	}

	// Read a key with a LeaseID
	req = logical.TestRequest(t, logical.ReadOperation, "secret/foo")
	req.ClientToken = root
	req.SetTokenEntry(&logical.TokenEntry{ID: root, NamespaceID: "root", Policies: []string{"root"}})
	resp, err = core.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil || resp.Secret == nil || resp.Secret.LeaseID == "" {
		t.Fatalf("bad: %#v", resp)
	}

	// Test the other route path
	req2 = logical.TestRequest(t, logical.UpdateOperation, "leases/revoke")
	req2.Data["lease_id"] = resp.Secret.LeaseID
	resp2, err = b.HandleRequest(namespace.RootContext(nil), req2)
	if err != nil {
		t.Fatalf("err: %v %#v", err, resp2)
	}
	if resp2 != nil {
		t.Fatalf("bad: %#v", resp)
	}
}

func TestSystemBackend_revoke_invalidID(t *testing.T) {
	b := testSystemBackend(t)

	// Attempt revoke
	req := logical.TestRequest(t, logical.UpdateOperation, "leases/revoke/foobarbaz")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %v", resp)
	}

	// Attempt revoke with other method
	req = logical.TestRequest(t, logical.UpdateOperation, "leases/revoke")
	req.Data["lease_id"] = "foobarbaz"
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %v", resp)
	}
}

func TestSystemBackend_revoke_invalidID_origUrl(t *testing.T) {
	b := testSystemBackend(t)

	// Attempt revoke
	req := logical.TestRequest(t, logical.UpdateOperation, "revoke/foobarbaz")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %v", resp)
	}

	// Attempt revoke with other method
	req = logical.TestRequest(t, logical.UpdateOperation, "revoke")
	req.Data["lease_id"] = "foobarbaz"
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %v", resp)
	}
}

func TestSystemBackend_revokePrefix(t *testing.T) {
	core, b, root := testCoreSystemBackend(t)

	// Create a key with a lease
	req := logical.TestRequest(t, logical.UpdateOperation, "secret/foo")
	req.Data["foo"] = "bar"
	req.Data["lease"] = "1h"
	req.ClientToken = root
	resp, err := core.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %#v", resp)
	}

	// Read a key with a LeaseID
	req = logical.TestRequest(t, logical.ReadOperation, "secret/foo")
	req.ClientToken = root
	req.SetTokenEntry(&logical.TokenEntry{ID: root, NamespaceID: "root", Policies: []string{"root"}})
	resp, err = core.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil || resp.Secret == nil || resp.Secret.LeaseID == "" {
		t.Fatalf("bad: %#v", resp)
	}

	// Attempt revoke
	req2 := logical.TestRequest(t, logical.UpdateOperation, "leases/revoke-prefix/secret/")
	resp2, err := b.HandleRequest(namespace.RootContext(nil), req2)
	if err != nil {
		t.Fatalf("err: %v %#v", err, resp2)
	}
	if resp2 != nil {
		t.Fatalf("bad: %#v", resp)
	}

	// Attempt renew
	req3 := logical.TestRequest(t, logical.UpdateOperation, "leases/renew/"+resp.Secret.LeaseID)
	resp3, err := b.HandleRequest(namespace.RootContext(nil), req3)
	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if resp3.Data["error"] != "lease not found" {
		t.Fatalf("bad: %v", *resp3)
	}
}

func TestSystemBackend_revokePrefix_origUrl(t *testing.T) {
	core, b, root := testCoreSystemBackend(t)

	// Create a key with a lease
	req := logical.TestRequest(t, logical.UpdateOperation, "secret/foo")
	req.Data["foo"] = "bar"
	req.Data["lease"] = "1h"
	req.ClientToken = root
	resp, err := core.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %#v", resp)
	}

	// Read a key with a LeaseID
	req = logical.TestRequest(t, logical.ReadOperation, "secret/foo")
	req.ClientToken = root
	req.SetTokenEntry(&logical.TokenEntry{ID: root, NamespaceID: "root", Policies: []string{"root"}})
	resp, err = core.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil || resp.Secret == nil || resp.Secret.LeaseID == "" {
		t.Fatalf("bad: %#v", resp)
	}

	// Attempt revoke
	req2 := logical.TestRequest(t, logical.UpdateOperation, "revoke-prefix/secret/")
	resp2, err := b.HandleRequest(namespace.RootContext(nil), req2)
	if err != nil {
		t.Fatalf("err: %v %#v", err, resp2)
	}
	if resp2 != nil {
		t.Fatalf("bad: %#v", resp)
	}

	// Attempt renew
	req3 := logical.TestRequest(t, logical.UpdateOperation, "renew/"+resp.Secret.LeaseID)
	resp3, err := b.HandleRequest(namespace.RootContext(nil), req3)
	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if resp3.Data["error"] != "lease not found" {
		t.Fatalf("bad: %#v", *resp3)
	}
}

func TestSystemBackend_revokePrefixAuth_newUrl(t *testing.T) {
	core, _, _ := TestCoreUnsealed(t)

	ts := core.tokenStore
	bc := &logical.BackendConfig{
		Logger: core.logger,
		System: logical.StaticSystemView{
			DefaultLeaseTTLVal: time.Hour * 24,
			MaxLeaseTTLVal:     time.Hour * 24 * 32,
		},
	}
	b := NewSystemBackend(core, hclog.New(&hclog.LoggerOptions{}))
	err := b.Backend.Setup(namespace.RootContext(nil), bc)
	if err != nil {
		t.Fatal(err)
	}

	exp := ts.expiration

	te := &logical.TokenEntry{
		ID:          "foo",
		Path:        "auth/github/login/bar",
		TTL:         time.Hour,
		NamespaceID: namespace.RootNamespaceID,
	}
	testMakeTokenDirectly(t, ts, te)

	te, err = ts.Lookup(namespace.RootContext(nil), "foo")
	if err != nil {
		t.Fatal(err)
	}
	if te == nil {
		t.Fatal("token entry was nil")
	}

	// Create a new token
	auth := &logical.Auth{
		ClientToken: te.ID,
		LeaseOptions: logical.LeaseOptions{
			TTL: time.Hour,
		},
	}
	err = exp.RegisterAuth(namespace.RootContext(nil), te, auth)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	req := logical.TestRequest(t, logical.UpdateOperation, "leases/revoke-prefix/auth/github/")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v %v", err, resp)
	}
	if resp != nil {
		t.Fatalf("bad: %#v", resp)
	}

	te, err = ts.Lookup(namespace.RootContext(nil), te.ID)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if te != nil {
		t.Fatalf("bad: %v", te)
	}
}

func TestSystemBackend_revokePrefixAuth_origUrl(t *testing.T) {
	core, _, _ := TestCoreUnsealed(t)
	ts := core.tokenStore
	bc := &logical.BackendConfig{
		Logger: core.logger,
		System: logical.StaticSystemView{
			DefaultLeaseTTLVal: time.Hour * 24,
			MaxLeaseTTLVal:     time.Hour * 24 * 32,
		},
	}
	b := NewSystemBackend(core, hclog.New(&hclog.LoggerOptions{}))
	err := b.Backend.Setup(namespace.RootContext(nil), bc)
	if err != nil {
		t.Fatal(err)
	}

	exp := ts.expiration

	te := &logical.TokenEntry{
		ID:          "foo",
		Path:        "auth/github/login/bar",
		TTL:         time.Hour,
		NamespaceID: namespace.RootNamespaceID,
	}
	testMakeTokenDirectly(t, ts, te)

	te, err = ts.Lookup(namespace.RootContext(nil), "foo")
	if err != nil {
		t.Fatal(err)
	}
	if te == nil {
		t.Fatal("token entry was nil")
	}

	// Create a new token
	auth := &logical.Auth{
		ClientToken: te.ID,
		LeaseOptions: logical.LeaseOptions{
			TTL: time.Hour,
		},
	}
	err = exp.RegisterAuth(namespace.RootContext(nil), te, auth)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	req := logical.TestRequest(t, logical.UpdateOperation, "revoke-prefix/auth/github/")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v %v", err, resp)
	}
	if resp != nil {
		t.Fatalf("bad: %#v", resp)
	}

	te, err = ts.Lookup(namespace.RootContext(nil), te.ID)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if te != nil {
		t.Fatalf("bad: %v", te)
	}
}

func TestSystemBackend_authTable(t *testing.T) {
	b := testSystemBackend(t)
	req := logical.TestRequest(t, logical.ReadOperation, "auth")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	exp := map[string]interface{}{
		"token/": map[string]interface{}{
			"type":                    "token",
			"external_entropy_access": false,
			"description":             "token based credentials",
			"accessor":                resp.Data["token/"].(map[string]interface{})["accessor"],
			"uuid":                    resp.Data["token/"].(map[string]interface{})["uuid"],
			"config": map[string]interface{}{
				"default_lease_ttl": int64(0),
				"max_lease_ttl":     int64(0),
				"force_no_cache":    false,
				"token_type":        "default-service",
			},
			"local":     false,
			"seal_wrap": false,
			"options":   map[string]string(nil),
		},
	}
	if diff := deep.Equal(resp.Data, exp); diff != nil {
		t.Fatal(diff)
	}

	req = logical.TestRequest(t, logical.ReadOperation, "auth/token")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if diff := deep.Equal(resp.Data, exp["token/"]); diff != nil {
		t.Fatal(diff)
	}
}

func TestSystemBackend_enableAuth(t *testing.T) {
	c, b, _ := testCoreSystemBackend(t)
	c.credentialBackends["noop"] = func(context.Context, *logical.BackendConfig) (logical.Backend, error) {
		return &NoopBackend{BackendType: logical.TypeCredential}, nil
	}

	req := logical.TestRequest(t, logical.UpdateOperation, "auth/foo")
	req.Data["type"] = "noop"
	req.Data["config"] = map[string]interface{}{
		"default_lease_ttl": "35m",
		"max_lease_ttl":     "45m",
	}
	req.Data["local"] = true
	req.Data["seal_wrap"] = true

	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %v", resp)
	}

	req = logical.TestRequest(t, logical.ReadOperation, "auth")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil {
		t.Fatal("resp is nil")
	}

	exp := map[string]interface{}{
		"foo/": map[string]interface{}{
			"type":                    "noop",
			"external_entropy_access": false,
			"description":             "",
			"accessor":                resp.Data["foo/"].(map[string]interface{})["accessor"],
			"uuid":                    resp.Data["foo/"].(map[string]interface{})["uuid"],
			"config": map[string]interface{}{
				"default_lease_ttl": int64(2100),
				"max_lease_ttl":     int64(2700),
				"force_no_cache":    false,
				"token_type":        "default-service",
			},
			"local":     true,
			"seal_wrap": true,
			"options":   map[string]string{},
		},
		"token/": map[string]interface{}{
			"type":                    "token",
			"external_entropy_access": false,
			"description":             "token based credentials",
			"accessor":                resp.Data["token/"].(map[string]interface{})["accessor"],
			"uuid":                    resp.Data["token/"].(map[string]interface{})["uuid"],
			"config": map[string]interface{}{
				"default_lease_ttl": int64(0),
				"max_lease_ttl":     int64(0),
				"force_no_cache":    false,
				"token_type":        "default-service",
			},
			"local":     false,
			"seal_wrap": false,
			"options":   map[string]string(nil),
		},
	}
	if diff := deep.Equal(resp.Data, exp); diff != nil {
		t.Fatal(diff)
	}
}

func TestSystemBackend_enableAuth_invalid(t *testing.T) {
	b := testSystemBackend(t)
	req := logical.TestRequest(t, logical.UpdateOperation, "auth/foo")
	req.Data["type"] = "nope"
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if resp.Data["error"] != `plugin not found in the catalog: nope` {
		t.Fatalf("bad: %v", resp)
	}
}

func TestSystemBackend_disableAuth(t *testing.T) {
	c, b, _ := testCoreSystemBackend(t)
	c.credentialBackends["noop"] = func(context.Context, *logical.BackendConfig) (logical.Backend, error) {
		return &NoopBackend{}, nil
	}

	// Register the backend
	req := logical.TestRequest(t, logical.UpdateOperation, "auth/foo")
	req.Data["type"] = "noop"
	b.HandleRequest(namespace.RootContext(nil), req)

	// Deregister it
	req = logical.TestRequest(t, logical.DeleteOperation, "auth/foo")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %v", resp)
	}
}

func TestSystemBackend_tuneAuth(t *testing.T) {
	c, b, _ := testCoreSystemBackend(t)
	c.credentialBackends["noop"] = func(context.Context, *logical.BackendConfig) (logical.Backend, error) {
		return &NoopBackend{BackendType: logical.TypeCredential}, nil
	}

	req := logical.TestRequest(t, logical.ReadOperation, "auth/token/tune")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil {
		t.Fatal("resp is nil")
	}

	exp := map[string]interface{}{
		"description":       "token based credentials",
		"default_lease_ttl": int(2764800),
		"max_lease_ttl":     int(2764800),
		"force_no_cache":    false,
		"token_type":        "default-service",
	}

	if diff := deep.Equal(resp.Data, exp); diff != nil {
		t.Fatal(diff)
	}

	req = logical.TestRequest(t, logical.UpdateOperation, "auth/token/tune")
	req.Data["description"] = ""
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	req = logical.TestRequest(t, logical.ReadOperation, "auth/token/tune")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil {
		t.Fatal("resp is nil")
	}

	if resp.Data["description"] != "" {
		t.Fatalf("got: %#v expect: %#v", resp.Data["description"], "")
	}
}

func TestSystemBackend_policyList(t *testing.T) {
	b := testSystemBackend(t)
	req := logical.TestRequest(t, logical.ReadOperation, "policy")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	exp := map[string]interface{}{
		"keys":     []string{"default", "root"},
		"policies": []string{"default", "root"},
	}
	if !reflect.DeepEqual(resp.Data, exp) {
		t.Fatalf("got: %#v expect: %#v", resp.Data, exp)
	}
}

func TestSystemBackend_policyCRUD(t *testing.T) {
	b := testSystemBackend(t)

	// Create the policy
	rules := `path "foo/" { policy = "read" }`
	req := logical.TestRequest(t, logical.UpdateOperation, "policy/Foo")
	req.Data["rules"] = rules
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v %#v", err, resp)
	}
	if resp != nil && (resp.IsError() || len(resp.Data) > 0) {
		t.Fatalf("bad: %#v", resp)
	}

	// Read the policy
	req = logical.TestRequest(t, logical.ReadOperation, "policy/foo")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	exp := map[string]interface{}{
		"name":  "foo",
		"rules": rules,
	}
	if !reflect.DeepEqual(resp.Data, exp) {
		t.Fatalf("got: %#v expect: %#v", resp.Data, exp)
	}

	// Read, and make sure that case has been normalized
	req = logical.TestRequest(t, logical.ReadOperation, "policy/Foo")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	exp = map[string]interface{}{
		"name":  "foo",
		"rules": rules,
	}
	if !reflect.DeepEqual(resp.Data, exp) {
		t.Fatalf("got: %#v expect: %#v", resp.Data, exp)
	}

	// List the policies
	req = logical.TestRequest(t, logical.ReadOperation, "policy")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	exp = map[string]interface{}{
		"keys":     []string{"default", "foo", "root"},
		"policies": []string{"default", "foo", "root"},
	}
	if !reflect.DeepEqual(resp.Data, exp) {
		t.Fatalf("got: %#v expect: %#v", resp.Data, exp)
	}

	// Delete the policy
	req = logical.TestRequest(t, logical.DeleteOperation, "policy/foo")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %#v", resp)
	}

	// Read the policy (deleted)
	req = logical.TestRequest(t, logical.ReadOperation, "policy/foo")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %#v", resp)
	}

	// List the policies (deleted)
	req = logical.TestRequest(t, logical.ReadOperation, "policy")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	exp = map[string]interface{}{
		"keys":     []string{"default", "root"},
		"policies": []string{"default", "root"},
	}
	if !reflect.DeepEqual(resp.Data, exp) {
		t.Fatalf("got: %#v expect: %#v", resp.Data, exp)
	}
}

func TestSystemBackend_enableAudit(t *testing.T) {
	c, b, _ := testCoreSystemBackend(t)
	c.auditBackends["noop"] = func(ctx context.Context, config *audit.BackendConfig) (audit.Backend, error) {
		return &NoopAudit{
			Config: config,
		}, nil
	}

	req := logical.TestRequest(t, logical.UpdateOperation, "audit/foo")
	req.Data["type"] = "noop"

	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %v", resp)
	}
}

func TestSystemBackend_auditHash(t *testing.T) {
	c, b, _ := testCoreSystemBackend(t)
	c.auditBackends["noop"] = func(ctx context.Context, config *audit.BackendConfig) (audit.Backend, error) {
		view := &logical.InmemStorage{}
		view.Put(namespace.RootContext(nil), &logical.StorageEntry{
			Key:   "salt",
			Value: []byte("foo"),
		})
		config.SaltView = view
		config.SaltConfig = &salt.Config{
			HMAC:     sha256.New,
			HMACType: "hmac-sha256",
			Location: salt.DefaultLocation,
		}
		return &NoopAudit{
			Config: config,
		}, nil
	}

	req := logical.TestRequest(t, logical.UpdateOperation, "audit/foo")
	req.Data["type"] = "noop"

	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %v", resp)
	}

	req = logical.TestRequest(t, logical.UpdateOperation, "audit-hash/foo")
	req.Data["input"] = "bar"

	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil || resp.Data == nil {
		t.Fatalf("response or its data was nil")
	}
	hash, ok := resp.Data["hash"]
	if !ok {
		t.Fatalf("did not get hash back in response, response was %#v", resp.Data)
	}
	if hash.(string) != "hmac-sha256:f9320baf0249169e73850cd6156ded0106e2bb6ad8cab01b7bbbebe6d1065317" {
		t.Fatalf("bad hash back: %s", hash.(string))
	}
}

func TestSystemBackend_enableAudit_invalid(t *testing.T) {
	b := testSystemBackend(t)
	req := logical.TestRequest(t, logical.UpdateOperation, "audit/foo")
	req.Data["type"] = "nope"
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
	if resp.Data["error"] != `unknown backend type: "nope"` {
		t.Fatalf("bad: %v", resp)
	}
}

func TestSystemBackend_auditTable(t *testing.T) {
	c, b, _ := testCoreSystemBackend(t)
	c.auditBackends["noop"] = func(ctx context.Context, config *audit.BackendConfig) (audit.Backend, error) {
		return &NoopAudit{
			Config: config,
		}, nil
	}

	req := logical.TestRequest(t, logical.UpdateOperation, "audit/foo")
	req.Data["type"] = "noop"
	req.Data["description"] = "testing"
	req.Data["options"] = map[string]interface{}{
		"foo": "bar",
	}
	req.Data["local"] = true
	b.HandleRequest(namespace.RootContext(nil), req)

	req = logical.TestRequest(t, logical.ReadOperation, "audit")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	exp := map[string]interface{}{
		"foo/": map[string]interface{}{
			"path":        "foo/",
			"type":        "noop",
			"description": "testing",
			"options": map[string]string{
				"foo": "bar",
			},
			"local": true,
		},
	}
	if !reflect.DeepEqual(resp.Data, exp) {
		t.Fatalf("got: %#v expect: %#v", resp.Data, exp)
	}
}

func TestSystemBackend_disableAudit(t *testing.T) {
	c, b, _ := testCoreSystemBackend(t)
	c.auditBackends["noop"] = func(ctx context.Context, config *audit.BackendConfig) (audit.Backend, error) {
		return &NoopAudit{
			Config: config,
		}, nil
	}

	req := logical.TestRequest(t, logical.UpdateOperation, "audit/foo")
	req.Data["type"] = "noop"
	req.Data["description"] = "testing"
	req.Data["options"] = map[string]interface{}{
		"foo": "bar",
	}
	b.HandleRequest(namespace.RootContext(nil), req)

	// Deregister it
	req = logical.TestRequest(t, logical.DeleteOperation, "audit/foo")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %v", resp)
	}
}

func TestSystemBackend_rawRead_Compressed(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		b := testSystemBackendRaw(t)

		req := logical.TestRequest(t, logical.ReadOperation, "raw/core/mounts")
		resp, err := b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !strings.HasPrefix(resp.Data["value"].(string), `{"type":"mounts"`) {
			t.Fatalf("bad: %v", resp)
		}
	})

	t.Run("base64", func(t *testing.T) {
		b := testSystemBackendRaw(t)

		req := logical.TestRequest(t, logical.ReadOperation, "raw/core/mounts")
		req.Data = map[string]interface{}{
			"encoding": "base64",
		}
		resp, err := b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		if _, ok := resp.Data["value"].([]byte); !ok {
			t.Fatalf("value is a not an array of bytes, it is %T", resp.Data["value"])
		}

		if !strings.HasPrefix(string(resp.Data["value"].([]byte)), `{"type":"mounts"`) {
			t.Fatalf("bad: %v", resp)
		}
	})

	t.Run("invalid_encoding", func(t *testing.T) {
		b := testSystemBackendRaw(t)

		req := logical.TestRequest(t, logical.ReadOperation, "raw/core/mounts")
		req.Data = map[string]interface{}{
			"encoding": "invalid_encoding",
		}
		resp, err := b.HandleRequest(namespace.RootContext(nil), req)
		if err != logical.ErrInvalidRequest {
			t.Fatalf("err: %v", err)
		}

		if !resp.IsError() {
			t.Fatalf("bad: %v", resp)
		}
	})

	t.Run("compressed_false", func(t *testing.T) {
		b := testSystemBackendRaw(t)

		req := logical.TestRequest(t, logical.ReadOperation, "raw/core/mounts")
		req.Data = map[string]interface{}{
			"compressed": false,
		}
		resp, err := b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		if _, ok := resp.Data["value"].(string); !ok {
			t.Fatalf("value is a not a string, it is %T", resp.Data["value"])
		}

		if !strings.HasPrefix(string(resp.Data["value"].(string)), string(compressutil.CompressionCanaryGzip)) {
			t.Fatalf("bad: %v", resp)
		}
	})

	t.Run("compressed_false_base64", func(t *testing.T) {
		b := testSystemBackendRaw(t)

		req := logical.TestRequest(t, logical.ReadOperation, "raw/core/mounts")
		req.Data = map[string]interface{}{
			"compressed": false,
			"encoding":   "base64",
		}

		resp, err := b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		if _, ok := resp.Data["value"].([]byte); !ok {
			t.Fatalf("value is a not an array of bytes, it is %T", resp.Data["value"])
		}

		if !strings.HasPrefix(string(resp.Data["value"].([]byte)), string(compressutil.CompressionCanaryGzip)) {
			t.Fatalf("bad: %v", resp)
		}
	})

	t.Run("uncompressed_entry_with_prefix_byte", func(t *testing.T) {
		b := testSystemBackendRaw(t)
		req := logical.TestRequest(t, logical.CreateOperation, "raw/test_raw")
		req.Data = map[string]interface{}{
			"value": "414c1e7f-0a9a-49e0-9fc4-61af329d0724",
		}

		resp, err := b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if resp != nil {
			t.Fatalf("bad: %v", resp)
		}

		req = logical.TestRequest(t, logical.ReadOperation, "raw/test_raw")
		resp, err = b.HandleRequest(namespace.RootContext(nil), req)
		if err == nil {
			t.Fatalf("expected error if trying to read uncompressed entry with prefix byte")
		}
		if !resp.IsError() {
			t.Fatalf("bad: %v", resp)
		}

		req = logical.TestRequest(t, logical.ReadOperation, "raw/test_raw")
		req.Data = map[string]interface{}{
			"compressed": false,
		}
		resp, err = b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if resp.IsError() {
			t.Fatalf("bad: %v", resp)
		}
		if resp.Data["value"].(string) != "414c1e7f-0a9a-49e0-9fc4-61af329d0724" {
			t.Fatalf("bad: %v", resp)
		}
	})
}

func TestSystemBackend_rawRead_Protected(t *testing.T) {
	b := testSystemBackendRaw(t)

	req := logical.TestRequest(t, logical.ReadOperation, "raw/"+keyringPath)
	_, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
}

func TestSystemBackend_rawWrite_Protected(t *testing.T) {
	b := testSystemBackendRaw(t)

	req := logical.TestRequest(t, logical.UpdateOperation, "raw/"+keyringPath)
	_, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
}

func TestSystemBackend_rawReadWrite(t *testing.T) {
	_, b, _ := testCoreSystemBackendRaw(t)

	req := logical.TestRequest(t, logical.CreateOperation, "raw/sys/policy/test")
	req.Data["value"] = `path "secret/" { policy = "read" }`
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %v", resp)
	}

	// Read via raw API
	req = logical.TestRequest(t, logical.ReadOperation, "raw/sys/policy/test")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.HasPrefix(resp.Data["value"].(string), "path") {
		t.Fatalf("bad: %v", resp)
	}

	// Note: since the upgrade code is gone that upgraded from 0.1, we can't
	// simply parse this out directly via GetPolicy, so the test now ends here.
}

func TestSystemBackend_rawWrite_ExistanceCheck(t *testing.T) {
	b := testSystemBackendRaw(t)
	req := logical.TestRequest(t, logical.CreateOperation, "raw/core/mounts")
	_, exist, err := b.HandleExistenceCheck(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: #{err}")
	}
	if !exist {
		t.Fatalf("raw existence check failed for actual key")
	}

	req = logical.TestRequest(t, logical.CreateOperation, "raw/non_existent")
	_, exist, err = b.HandleExistenceCheck(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: #{err}")
	}
	if exist {
		t.Fatalf("raw existence check failed for non-existent key")
	}
}

func TestSystemBackend_rawReadWrite_base64(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		_, b, _ := testCoreSystemBackendRaw(t)

		req := logical.TestRequest(t, logical.CreateOperation, "raw/sys/policy/test")
		req.Data = map[string]interface{}{
			"value":    base64.StdEncoding.EncodeToString([]byte(`path "secret/" { policy = "read"[ }`)),
			"encoding": "base64",
		}
		resp, err := b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if resp != nil {
			t.Fatalf("bad: %v", resp)
		}

		// Read via raw API
		req = logical.TestRequest(t, logical.ReadOperation, "raw/sys/policy/test")
		resp, err = b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !strings.HasPrefix(resp.Data["value"].(string), "path") {
			t.Fatalf("bad: %v", resp)
		}
	})

	t.Run("invalid_value", func(t *testing.T) {
		_, b, _ := testCoreSystemBackendRaw(t)

		req := logical.TestRequest(t, logical.CreateOperation, "raw/sys/policy/test")
		req.Data = map[string]interface{}{
			"value":    "invalid base64",
			"encoding": "base64",
		}
		resp, err := b.HandleRequest(namespace.RootContext(nil), req)
		if err == nil {
			t.Fatalf("no error")
		}

		if err != logical.ErrInvalidRequest {
			t.Fatalf("unexpected error: %v", err)
		}

		if !resp.IsError() {
			t.Fatalf("response is not error: %v", resp)
		}
	})

	t.Run("invalid_encoding", func(t *testing.T) {
		_, b, _ := testCoreSystemBackendRaw(t)

		req := logical.TestRequest(t, logical.CreateOperation, "raw/sys/policy/test")
		req.Data = map[string]interface{}{
			"value":    "text",
			"encoding": "invalid_encoding",
		}
		resp, err := b.HandleRequest(namespace.RootContext(nil), req)
		if err == nil {
			t.Fatalf("no error")
		}

		if err != logical.ErrInvalidRequest {
			t.Fatalf("unexpected error: %v", err)
		}

		if !resp.IsError() {
			t.Fatalf("response is not error: %v", resp)
		}
	})
}

func TestSystemBackend_rawReadWrite_Compressed(t *testing.T) {
	t.Run("use_existing_compression", func(t *testing.T) {
		b := testSystemBackendRaw(t)

		req := logical.TestRequest(t, logical.ReadOperation, "raw/core/mounts")
		resp, err := b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		mounts := resp.Data["value"].(string)
		req = logical.TestRequest(t, logical.UpdateOperation, "raw/core/mounts")
		req.Data = map[string]interface{}{
			"value":            mounts,
			"compression_type": compressutil.CompressionTypeGzip,
		}
		resp, err = b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		// Read back and check gzip was applied by looking for prefix byte
		req = logical.TestRequest(t, logical.ReadOperation, "raw/core/mounts")
		req.Data = map[string]interface{}{
			"compressed": false,
			"encoding":   "base64",
		}

		resp, err = b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		if _, ok := resp.Data["value"].([]byte); !ok {
			t.Fatalf("value is a not an array of bytes, it is %T", resp.Data["value"])
		}

		if !strings.HasPrefix(string(resp.Data["value"].([]byte)), string(compressutil.CompressionCanaryGzip)) {
			t.Fatalf("bad: %v", resp)
		}
	})

	t.Run("compression_type_matches_existing_compression", func(t *testing.T) {
		b := testSystemBackendRaw(t)

		req := logical.TestRequest(t, logical.ReadOperation, "raw/core/mounts")
		resp, err := b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		mounts := resp.Data["value"].(string)
		req = logical.TestRequest(t, logical.UpdateOperation, "raw/core/mounts")
		req.Data = map[string]interface{}{
			"value": mounts,
		}
		resp, err = b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		// Read back and check gzip was applied by looking for prefix byte
		req = logical.TestRequest(t, logical.ReadOperation, "raw/core/mounts")
		req.Data = map[string]interface{}{
			"compressed": false,
			"encoding":   "base64",
		}
		resp, err = b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		if _, ok := resp.Data["value"].([]byte); !ok {
			t.Fatalf("value is a not an array of bytes, it is %T", resp.Data["value"])
		}

		if !strings.HasPrefix(string(resp.Data["value"].([]byte)), string(compressutil.CompressionCanaryGzip)) {
			t.Fatalf("bad: %v", resp)
		}
	})

	t.Run("write_uncompressed_over_existing_compressed", func(t *testing.T) {
		b := testSystemBackendRaw(t)

		req := logical.TestRequest(t, logical.ReadOperation, "raw/core/mounts")
		resp, err := b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		mounts := resp.Data["value"].(string)
		req = logical.TestRequest(t, logical.UpdateOperation, "raw/core/mounts")
		req.Data = map[string]interface{}{
			"value":            mounts,
			"compression_type": "",
		}
		resp, err = b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		// Read back and check gzip was not applied by looking for prefix byte
		req = logical.TestRequest(t, logical.ReadOperation, "raw/core/mounts")
		req.Data = map[string]interface{}{
			"compressed": false,
			"encoding":   "base64",
		}

		resp, err = b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		if _, ok := resp.Data["value"].([]byte); !ok {
			t.Fatalf("value is a not an array of bytes, it is %T", resp.Data["value"])
		}

		if !strings.HasPrefix(string(resp.Data["value"].([]byte)), `{"type":"mounts"`) {
			t.Fatalf("bad: %v", resp)
		}
	})

	t.Run("invalid_compression_type", func(t *testing.T) {
		b := testSystemBackendRaw(t)

		req := logical.TestRequest(t, logical.ReadOperation, "raw/core/mounts")
		resp, err := b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("err: %v", err)
		}

		mounts := resp.Data["value"].(string)
		req = logical.TestRequest(t, logical.UpdateOperation, "raw/core/mounts")
		req.Data = map[string]interface{}{
			"value":            mounts,
			"compression_type": "invalid_type",
		}
		resp, err = b.HandleRequest(namespace.RootContext(nil), req)
		if err != logical.ErrInvalidRequest {
			t.Fatalf("unexpected error: %v", err)
		}

		if !resp.IsError() {
			t.Fatalf("response is not error: %v", resp)
		}
	})

	t.Run("update_non_existent_entry", func(t *testing.T) {
		b := testSystemBackendRaw(t)

		req := logical.TestRequest(t, logical.UpdateOperation, "raw/non_existent")
		req.Data = map[string]interface{}{
			"value": "{}",
		}
		resp, err := b.HandleRequest(namespace.RootContext(nil), req)
		if err != logical.ErrInvalidRequest {
			t.Fatalf("unexpected error: %v", err)
		}

		if !resp.IsError() {
			t.Fatalf("response is not error: %v", resp)
		}
	})

	t.Run("invalid_compression_over_existing_uncompressed_data", func(t *testing.T) {
		b := testSystemBackendRaw(t)

		req := logical.TestRequest(t, logical.CreateOperation, "raw/test")
		req.Data = map[string]interface{}{
			"value": "{}",
		}
		resp, err := b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if resp.IsError() {
			t.Fatalf("response is an error: %v", resp)
		}

		req = logical.TestRequest(t, logical.UpdateOperation, "raw/test")
		req.Data = map[string]interface{}{
			"value":            "{}",
			"compression_type": compressutil.CompressionTypeGzip,
		}
		resp, err = b.HandleRequest(namespace.RootContext(nil), req)
		if err != logical.ErrInvalidRequest {
			t.Fatalf("unexpected error: %v", err)
		}

		if !resp.IsError() {
			t.Fatalf("response is not error: %v", resp)
		}
	})

	t.Run("wrong_compression_type_over_existing_compressed_data", func(t *testing.T) {
		b := testSystemBackendRaw(t)

		req := logical.TestRequest(t, logical.CreateOperation, "raw/test")
		req.Data = map[string]interface{}{
			"value":            "{}",
			"compression_type": compressutil.CompressionTypeGzip,
		}
		resp, err := b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if resp.IsError() {
			t.Fatalf("response is an error: %v", resp)
		}

		req = logical.TestRequest(t, logical.UpdateOperation, "raw/test")
		req.Data = map[string]interface{}{
			"value":            "{}",
			"compression_type": compressutil.CompressionTypeSnappy,
		}
		resp, err = b.HandleRequest(namespace.RootContext(nil), req)
		if err != logical.ErrInvalidRequest {
			t.Fatalf("unexpected error: %v", err)
		}

		if !resp.IsError() {
			t.Fatalf("response is not error: %v", resp)
		}
	})
}

func TestSystemBackend_rawDelete_Protected(t *testing.T) {
	b := testSystemBackendRaw(t)

	req := logical.TestRequest(t, logical.DeleteOperation, "raw/"+keyringPath)
	_, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != logical.ErrInvalidRequest {
		t.Fatalf("err: %v", err)
	}
}

func TestSystemBackend_rawDelete(t *testing.T) {
	c, b, _ := testCoreSystemBackendRaw(t)

	// set the policy!
	p := &Policy{
		Name:      "test",
		Type:      PolicyTypeACL,
		namespace: namespace.RootNamespace,
	}
	err := c.policyStore.SetPolicy(namespace.RootContext(nil), p)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	// Delete the policy
	req := logical.TestRequest(t, logical.DeleteOperation, "raw/sys/policy/test")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %v", resp)
	}

	// Policy should be gone
	c.policyStore.tokenPoliciesLRU.Purge()
	out, err := c.policyStore.GetPolicy(namespace.RootContext(nil), "test", PolicyTypeToken)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out != nil {
		t.Fatalf("policy should be gone")
	}
}

func TestSystemBackend_keyStatus(t *testing.T) {
	b := testSystemBackend(t)
	req := logical.TestRequest(t, logical.ReadOperation, "key-status")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	exp := map[string]interface{}{
		"term": 1,
	}
	delete(resp.Data, "install_time")
	delete(resp.Data, "encryptions")
	if !reflect.DeepEqual(resp.Data, exp) {
		t.Fatalf("got: %#v expect: %#v", resp.Data, exp)
	}
}

func TestSystemBackend_rotateConfig(t *testing.T) {
	b := testSystemBackend(t)
	req := logical.TestRequest(t, logical.ReadOperation, "rotate/config")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	exp := map[string]interface{}{
		"max_operations": absoluteOperationMaximum,
		"interval":       0,
		"enabled":        true,
	}
	if !reflect.DeepEqual(resp.Data, exp) {
		t.Fatalf("got: %#v expect: %#v", resp.Data, exp)
	}

	req2 := logical.TestRequest(t, logical.UpdateOperation, "rotate/config")
	req2.Data["max_operations"] = int64(3221225472)
	req2.Data["interval"] = "5432h0m0s"
	req2.Data["enabled"] = false

	resp, err = b.HandleRequest(namespace.RootContext(nil), req2)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	exp = map[string]interface{}{
		"max_operations": int64(3221225472),
		"interval":       "5432h0m0s",
		"enabled":        false,
	}

	if !reflect.DeepEqual(resp.Data, exp) {
		t.Fatalf("got: %#v expect: %#v", resp.Data, exp)
	}
}

func TestSystemBackend_rotate(t *testing.T) {
	b := testSystemBackend(t)

	req := logical.TestRequest(t, logical.UpdateOperation, "rotate")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp != nil {
		t.Fatalf("bad: %v", resp)
	}

	req = logical.TestRequest(t, logical.ReadOperation, "key-status")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	exp := map[string]interface{}{
		"term": 2,
	}
	delete(resp.Data, "install_time")
	delete(resp.Data, "encryptions")
	if !reflect.DeepEqual(resp.Data, exp) {
		t.Fatalf("got: %#v expect: %#v", resp.Data, exp)
	}
}

func testSystemBackend(t *testing.T) logical.Backend {
	c, _, _ := TestCoreUnsealed(t)
	return c.systemBackend
}

func testSystemBackendRaw(t *testing.T) logical.Backend {
	c, _, _ := TestCoreUnsealedRaw(t)
	return c.systemBackend
}

func testCoreSystemBackend(t *testing.T) (*Core, logical.Backend, string) {
	c, _, root := TestCoreUnsealed(t)
	return c, c.systemBackend, root
}

func testCoreSystemBackendRaw(t *testing.T) (*Core, logical.Backend, string) {
	c, _, root := TestCoreUnsealedRaw(t)
	return c, c.systemBackend, root
}

func TestSystemBackend_PluginCatalog_CRUD(t *testing.T) {
	c, b, _ := testCoreSystemBackend(t)
	// Bootstrap the pluginCatalog
	sym, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	c.pluginCatalog.directory = sym

	req := logical.TestRequest(t, logical.ListOperation, "plugins/catalog/database")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if len(resp.Data["keys"].([]string)) != len(c.builtinRegistry.Keys(consts.PluginTypeDatabase)) {
		t.Fatalf("Wrong number of plugins, got %d, expected %d", len(resp.Data["keys"].([]string)), len(builtinplugins.Registry.Keys(consts.PluginTypeDatabase)))
	}

	req = logical.TestRequest(t, logical.ReadOperation, "plugins/catalog/database/mysql-database-plugin")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	actualRespData := resp.Data
	expectedRespData := map[string]interface{}{
		"name":    "mysql-database-plugin",
		"command": "",
		"args":    []string(nil),
		"sha256":  "",
		"builtin": true,
	}
	if !reflect.DeepEqual(actualRespData, expectedRespData) {
		t.Fatalf("expected did not match actual, got %#v\n expected %#v\n", actualRespData, expectedRespData)
	}

	// Set a plugin
	file, err := ioutil.TempFile(os.TempDir(), "temp")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	// Check we can only specify args in one of command or args.
	command := fmt.Sprintf("%s --test", filepath.Base(file.Name()))
	req = logical.TestRequest(t, logical.UpdateOperation, "plugins/catalog/database/test-plugin")
	req.Data["args"] = []string{"--foo"}
	req.Data["sha_256"] = hex.EncodeToString([]byte{'1'})
	req.Data["command"] = command
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Error().Error() != "must not specify args in command and args field" {
		t.Fatalf("err: %v", resp.Error())
	}

	delete(req.Data, "args")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil || resp.Error() != nil {
		t.Fatalf("err: %v %v", err, resp.Error())
	}

	req = logical.TestRequest(t, logical.ReadOperation, "plugins/catalog/database/test-plugin")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	actual := resp.Data
	expected := map[string]interface{}{
		"name":    "test-plugin",
		"command": filepath.Base(file.Name()),
		"args":    []string{"--test"},
		"sha256":  "31",
		"builtin": false,
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("expected did not match actual, got %#v\n expected %#v\n", actual, expected)
	}

	// Delete plugin
	req = logical.TestRequest(t, logical.DeleteOperation, "plugins/catalog/database/test-plugin")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	req = logical.TestRequest(t, logical.ReadOperation, "plugins/catalog/database/test-plugin")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if resp != nil || err != nil {
		t.Fatalf("expected nil response, plugin not deleted correctly got resp: %v, err: %v", resp, err)
	}
}

func TestSystemBackend_ToolsHash(t *testing.T) {
	b := testSystemBackend(t)
	req := logical.TestRequest(t, logical.UpdateOperation, "tools/hash")
	req.Data = map[string]interface{}{
		"input": "dGhlIHF1aWNrIGJyb3duIGZveA==",
	}
	_, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	doRequest := func(req *logical.Request, errExpected bool, expected string) {
		t.Helper()
		resp, err := b.HandleRequest(namespace.RootContext(nil), req)
		if err != nil && !errExpected {
			t.Fatal(err)
		}
		if resp == nil {
			t.Fatal("expected non-nil response")
		}
		if errExpected {
			if !resp.IsError() {
				t.Fatalf("bad: got error response: %#v", *resp)
			}
			return
		}
		if resp.IsError() {
			t.Fatalf("bad: got error response: %#v", *resp)
		}
		sum, ok := resp.Data["sum"]
		if !ok {
			t.Fatal("no sum key found in returned data")
		}
		if sum.(string) != expected {
			t.Fatalf("mismatched hashes: got: %s, expect: %s", sum.(string), expected)
		}
	}

	// Test defaults -- sha2-256
	doRequest(req, false, "9ecb36561341d18eb65484e833efea61edc74b84cf5e6ae1b81c63533e25fc8f")

	// Test algorithm selection in the path
	req.Path = "tools/hash/sha2-224"
	doRequest(req, false, "ea074a96cabc5a61f8298a2c470f019074642631a49e1c5e2f560865")

	// Reset and test algorithm selection in the data
	req.Path = "tools/hash"
	req.Data["algorithm"] = "sha2-224"
	doRequest(req, false, "ea074a96cabc5a61f8298a2c470f019074642631a49e1c5e2f560865")

	req.Data["algorithm"] = "sha2-384"
	doRequest(req, false, "15af9ec8be783f25c583626e9491dbf129dd6dd620466fdf05b3a1d0bb8381d30f4d3ec29f923ff1e09a0f6b337365a6")

	req.Data["algorithm"] = "sha2-512"
	doRequest(req, false, "d9d380f29b97ad6a1d92e987d83fa5a02653301e1006dd2bcd51afa59a9147e9caedaf89521abc0f0b682adcd47fb512b8343c834a32f326fe9bef00542ce887")

	// Test returning as base64
	req.Data["format"] = "base64"
	doRequest(req, false, "2dOA8puXrWodkumH2D+loCZTMB4QBt0rzVGvpZqRR+nK7a+JUhq8DwtoKtzUf7USuDQ8g0oy8yb+m+8AVCzohw==")

	// Test SHA-3
	req.Data["format"] = "hex"
	req.Data["algorithm"] = "sha3-224"
	doRequest(req, false, "ced91e69d89c837e87cff960bd64fd9b9f92325fb9add8988d33d007")

	req.Data["algorithm"] = "sha3-256"
	doRequest(req, false, "e4bd866ec3fa52df3b7842aa97b448bc859a7606cefcdad1715847f4b82a6c93")

	req.Data["algorithm"] = "sha3-384"
	doRequest(req, false, "715cd38cbf8d0bab426b6a084d649760be555dd64b34de6db148a3fbf2cd2aa5d8b03eb6eda73a3e9a8769c00b4c2113")

	req.Data["algorithm"] = "sha3-512"
	doRequest(req, false, "f7cac5ad830422a5408b36a60a60620687be180765a3e2895bc3bdbd857c9e08246c83064d4e3612f0cb927f3ead208413ab98624bf7b0617af0f03f62080976")

	// Test bad input/format/algorithm
	req.Data["format"] = "base92"
	doRequest(req, true, "")

	req.Data["format"] = "hex"
	req.Data["algorithm"] = "foobar"
	doRequest(req, true, "")

	req.Data["algorithm"] = "sha2-256"
	req.Data["input"] = "foobar"
	doRequest(req, true, "")
}

func TestSystemBackend_ToolsRandom(t *testing.T) {
	b := testSystemBackend(t)
	req := logical.TestRequest(t, logical.UpdateOperation, "tools/random")

	_, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	doRequest := func(req *logical.Request, errExpected bool, format string, numBytes int) {
		t.Helper()
		getResponse := func() []byte {
			resp, err := b.HandleRequest(namespace.RootContext(nil), req)
			if err != nil && !errExpected {
				t.Fatal(err)
			}
			if resp == nil {
				t.Fatal("expected non-nil response")
			}
			if errExpected {
				if !resp.IsError() {
					t.Fatalf("bad: got error response: %#v", *resp)
				}
				return nil
			}
			if resp.IsError() {
				t.Fatalf("bad: got error response: %#v", *resp)
			}
			if _, ok := resp.Data["random_bytes"]; !ok {
				t.Fatal("no random_bytes found in response")
			}

			outputStr := resp.Data["random_bytes"].(string)
			var outputBytes []byte
			switch format {
			case "base64":
				outputBytes, err = base64.StdEncoding.DecodeString(outputStr)
			case "hex":
				outputBytes, err = hex.DecodeString(outputStr)
			default:
				t.Fatal("unknown format")
			}
			if err != nil {
				t.Fatal(err)
			}

			return outputBytes
		}

		rand1 := getResponse()
		// Expected error
		if rand1 == nil {
			return
		}
		rand2 := getResponse()
		if len(rand1) != numBytes || len(rand2) != numBytes {
			t.Fatal("length of output random bytes not what is expected")
		}
		if reflect.DeepEqual(rand1, rand2) {
			t.Fatal("found identical ouputs")
		}
	}

	// Test defaults
	doRequest(req, false, "base64", 32)

	// Test size selection in the path
	req.Path = "tools/random/24"
	req.Data["format"] = "hex"
	doRequest(req, false, "hex", 24)

	// Test bad input/format
	req.Path = "tools/random"
	req.Data["format"] = "base92"
	doRequest(req, true, "", 0)

	req.Data["format"] = "hex"
	req.Data["bytes"] = -1
	doRequest(req, true, "", 0)

	req.Data["format"] = "hex"
	req.Data["bytes"] = maxBytes + 1
	doRequest(req, true, "", 0)
}

func TestSystemBackend_InternalUIMounts(t *testing.T) {
	_, b, rootToken := testCoreSystemBackend(t)

	// Ensure no entries are in the endpoint as a starting point
	req := logical.TestRequest(t, logical.ReadOperation, "internal/ui/mounts")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	exp := map[string]interface{}{
		"secret": map[string]interface{}{},
		"auth":   map[string]interface{}{},
	}
	if !reflect.DeepEqual(resp.Data, exp) {
		t.Fatalf("got: %#v expect: %#v", resp.Data, exp)
	}

	req = logical.TestRequest(t, logical.ReadOperation, "internal/ui/mounts")
	req.ClientToken = rootToken
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	exp = map[string]interface{}{
		"secret": map[string]interface{}{
			"secret/": map[string]interface{}{
				"type":                    "kv",
				"external_entropy_access": false,
				"description":             "key/value secret storage",
				"accessor":                resp.Data["secret"].(map[string]interface{})["secret/"].(map[string]interface{})["accessor"],
				"uuid":                    resp.Data["secret"].(map[string]interface{})["secret/"].(map[string]interface{})["uuid"],
				"config": map[string]interface{}{
					"default_lease_ttl": resp.Data["secret"].(map[string]interface{})["secret/"].(map[string]interface{})["config"].(map[string]interface{})["default_lease_ttl"].(int64),
					"max_lease_ttl":     resp.Data["secret"].(map[string]interface{})["secret/"].(map[string]interface{})["config"].(map[string]interface{})["max_lease_ttl"].(int64),
					"force_no_cache":    false,
				},
				"local":     false,
				"seal_wrap": false,
				"options": map[string]string{
					"version": "1",
				},
			},
			"sys/": map[string]interface{}{
				"type":                    "system",
				"external_entropy_access": false,
				"description":             "system endpoints used for control, policy and debugging",
				"accessor":                resp.Data["secret"].(map[string]interface{})["sys/"].(map[string]interface{})["accessor"],
				"uuid":                    resp.Data["secret"].(map[string]interface{})["sys/"].(map[string]interface{})["uuid"],
				"config": map[string]interface{}{
					"default_lease_ttl":           resp.Data["secret"].(map[string]interface{})["sys/"].(map[string]interface{})["config"].(map[string]interface{})["default_lease_ttl"].(int64),
					"max_lease_ttl":               resp.Data["secret"].(map[string]interface{})["sys/"].(map[string]interface{})["config"].(map[string]interface{})["max_lease_ttl"].(int64),
					"force_no_cache":              false,
					"passthrough_request_headers": []string{"Accept"},
				},
				"local":     false,
				"seal_wrap": true,
				"options":   map[string]string(nil),
			},
			"cubbyhole/": map[string]interface{}{
				"description":             "per-token private secret storage",
				"type":                    "cubbyhole",
				"external_entropy_access": false,
				"accessor":                resp.Data["secret"].(map[string]interface{})["cubbyhole/"].(map[string]interface{})["accessor"],
				"uuid":                    resp.Data["secret"].(map[string]interface{})["cubbyhole/"].(map[string]interface{})["uuid"],
				"config": map[string]interface{}{
					"default_lease_ttl": resp.Data["secret"].(map[string]interface{})["cubbyhole/"].(map[string]interface{})["config"].(map[string]interface{})["default_lease_ttl"].(int64),
					"max_lease_ttl":     resp.Data["secret"].(map[string]interface{})["cubbyhole/"].(map[string]interface{})["config"].(map[string]interface{})["max_lease_ttl"].(int64),
					"force_no_cache":    false,
				},
				"local":     true,
				"seal_wrap": false,
				"options":   map[string]string(nil),
			},
			"identity/": map[string]interface{}{
				"description":             "identity store",
				"type":                    "identity",
				"external_entropy_access": false,
				"accessor":                resp.Data["secret"].(map[string]interface{})["identity/"].(map[string]interface{})["accessor"],
				"uuid":                    resp.Data["secret"].(map[string]interface{})["identity/"].(map[string]interface{})["uuid"],
				"config": map[string]interface{}{
					"default_lease_ttl":           resp.Data["secret"].(map[string]interface{})["identity/"].(map[string]interface{})["config"].(map[string]interface{})["default_lease_ttl"].(int64),
					"max_lease_ttl":               resp.Data["secret"].(map[string]interface{})["identity/"].(map[string]interface{})["config"].(map[string]interface{})["max_lease_ttl"].(int64),
					"force_no_cache":              false,
					"passthrough_request_headers": []string{"Authorization"},
				},
				"local":     false,
				"seal_wrap": false,
				"options":   map[string]string(nil),
			},
		},
		"auth": map[string]interface{}{
			"token/": map[string]interface{}{
				"options": map[string]string(nil),
				"config": map[string]interface{}{
					"default_lease_ttl": int64(0),
					"max_lease_ttl":     int64(0),
					"force_no_cache":    false,
					"token_type":        "default-service",
				},
				"type":                    "token",
				"external_entropy_access": false,
				"description":             "token based credentials",
				"accessor":                resp.Data["auth"].(map[string]interface{})["token/"].(map[string]interface{})["accessor"],
				"uuid":                    resp.Data["auth"].(map[string]interface{})["token/"].(map[string]interface{})["uuid"],
				"local":                   false,
				"seal_wrap":               false,
			},
		},
	}
	if diff := deep.Equal(resp.Data, exp); diff != nil {
		t.Fatal(diff)
	}

	// Mount-tune an auth mount
	req = logical.TestRequest(t, logical.UpdateOperation, "auth/token/tune")
	req.Data["listing_visibility"] = "unauth"
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if resp.IsError() || err != nil {
		t.Fatalf("resp.Error: %v, err:%v", resp.Error(), err)
	}

	// Mount-tune a secret mount
	req = logical.TestRequest(t, logical.UpdateOperation, "mounts/secret/tune")
	req.Data["listing_visibility"] = "unauth"
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if resp.IsError() || err != nil {
		t.Fatalf("resp.Error: %v, err:%v", resp.Error(), err)
	}

	req = logical.TestRequest(t, logical.ReadOperation, "internal/ui/mounts")
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	exp = map[string]interface{}{
		"secret": map[string]interface{}{
			"secret/": map[string]interface{}{
				"type":        "kv",
				"description": "key/value secret storage",
				"options":     map[string]string{"version": "1"},
			},
		},
		"auth": map[string]interface{}{
			"token/": map[string]interface{}{
				"type":        "token",
				"description": "token based credentials",
				"options":     map[string]string(nil),
			},
		},
	}
	if !reflect.DeepEqual(resp.Data, exp) {
		t.Fatalf("got: %#v expect: %#v", resp.Data, exp)
	}
}

func TestSystemBackend_InternalUIMount(t *testing.T) {
	core, b, rootToken := testCoreSystemBackend(t)

	req := logical.TestRequest(t, logical.UpdateOperation, "policy/secret")
	req.ClientToken = rootToken
	req.Data = map[string]interface{}{
		"rules": `path "secret/foo/*" {
    capabilities = ["create", "read", "update", "delete", "list"]
}`,
	}
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil || (resp != nil && resp.IsError()) {
		t.Fatalf("Bad %#v %#v", err, resp)
	}

	req = logical.TestRequest(t, logical.UpdateOperation, "mounts/kv")
	req.ClientToken = rootToken
	req.Data = map[string]interface{}{
		"type": "kv",
	}
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil || (resp != nil && resp.IsError()) {
		t.Fatalf("Bad %#v %#v", err, resp)
	}

	req = logical.TestRequest(t, logical.ReadOperation, "internal/ui/mounts/kv/bar")
	req.ClientToken = rootToken
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil || (resp != nil && resp.IsError()) {
		t.Fatalf("Bad %#v %#v", err, resp)
	}
	if resp.Data["type"] != "kv" {
		t.Fatalf("Bad Response: %#v", resp)
	}

	testMakeServiceTokenViaBackend(t, core.tokenStore, rootToken, "tokenid", "", []string{"secret"})

	req = logical.TestRequest(t, logical.ReadOperation, "internal/ui/mounts/kv")
	req.ClientToken = "tokenid"
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != logical.ErrPermissionDenied {
		t.Fatal("expected permission denied error")
	}

	req = logical.TestRequest(t, logical.ReadOperation, "internal/ui/mounts/secret")
	req.ClientToken = "tokenid"
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil || (resp != nil && resp.IsError()) {
		t.Fatalf("Bad %#v %#v", err, resp)
	}
	if resp.Data["type"] != "kv" {
		t.Fatalf("Bad Response: %#v", resp)
	}

	req = logical.TestRequest(t, logical.ReadOperation, "internal/ui/mounts/sys")
	req.ClientToken = "tokenid"
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil || (resp != nil && resp.IsError()) {
		t.Fatalf("Bad %#v %#v", err, resp)
	}
	if resp.Data["type"] != "system" {
		t.Fatalf("Bad Response: %#v", resp)
	}

	req = logical.TestRequest(t, logical.ReadOperation, "internal/ui/mounts/non-existent")
	req.ClientToken = "tokenid"
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != logical.ErrPermissionDenied {
		t.Fatal("expected permission denied error")
	}
}

func TestSystemBackend_OpenAPI(t *testing.T) {
	_, b, rootToken := testCoreSystemBackend(t)
	var oapi map[string]interface{}

	// Ensure no paths are reported if there is no token
	req := logical.TestRequest(t, logical.ReadOperation, "internal/specs/openapi")
	resp, err := b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	body := resp.Data["http_raw_body"].([]byte)
	err = jsonutil.DecodeJSON(body, &oapi)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	exp := map[string]interface{}{
		"openapi": framework.OASVersion,
		"info": map[string]interface{}{
			"title":       "HashiCorp Vault API",
			"description": "HTTP API that gives you full access to Vault. All API routes are prefixed with `/v1/`.",
			"version":     version.GetVersion().Version,
			"license": map[string]interface{}{
				"name": "Mozilla Public License 2.0",
				"url":  "https://www.mozilla.org/en-US/MPL/2.0",
			},
		},
		"paths": map[string]interface{}{},
	}

	if diff := deep.Equal(oapi, exp); diff != nil {
		t.Fatal(diff)
	}

	// Check that default paths are present with a root token
	req = logical.TestRequest(t, logical.ReadOperation, "internal/specs/openapi")
	req.ClientToken = rootToken
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	body = resp.Data["http_raw_body"].([]byte)
	err = jsonutil.DecodeJSON(body, &oapi)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	doc, err := framework.NewOASDocumentFromMap(oapi)
	if err != nil {
		t.Fatal(err)
	}

	pathSamples := []struct {
		path string
		tag  string
	}{
		{"/auth/token/lookup", "auth"},
		{"/cubbyhole/{path}", "secrets"},
		{"/identity/group/id", "identity"},
		{"/secret/.*", "secrets"}, // TODO update after kv repo update
		{"/sys/policy", "system"},
	}

	for _, path := range pathSamples {
		if doc.Paths[path.path] == nil {
			t.Fatalf("didn't find expected path '%s'.", path)
		}
		tag := doc.Paths[path.path].Get.Tags[0]
		if tag != path.tag {
			t.Fatalf("path: %s; expected tag: %s, actual: %s", path.path, tag, path.tag)
		}
	}

	// Simple sanity check of response size (which is much larger than most
	// Vault responses), mainly to catch mass omission of expected path data.
	minLen := 70000
	if len(body) < minLen {
		t.Fatalf("response size too small; expected: min %d, actual: %d", minLen, len(body))
	}

	// Test path-help response
	req = logical.TestRequest(t, logical.HelpOperation, "rotate")
	req.ClientToken = rootToken
	resp, err = b.HandleRequest(namespace.RootContext(nil), req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	doc = resp.Data["openapi"].(*framework.OASDocument)
	if len(doc.Paths) != 1 {
		t.Fatalf("expected 1 path, actual: %d", len(doc.Paths))
	}

	if doc.Paths["/rotate"] == nil {
		t.Fatalf("expected to find path '/rotate'")
	}
}

func TestSystemBackend_PathWildcardPreflight(t *testing.T) {
	core, b, _ := testCoreSystemBackend(t)

	ctx := namespace.RootContext(nil)

	// Add another mount
	me := &MountEntry{
		Table:   mountTableType,
		Path:    sanitizePath("kv-v1"),
		Type:    "kv",
		Options: map[string]string{"version": "1"},
	}
	if err := core.mount(ctx, me); err != nil {
		t.Fatal(err)
	}

	// Create the policy, designed to fail
	rules := `path "foo" { capabilities = ["read"] }`
	req := logical.TestRequest(t, logical.UpdateOperation, "policy/foo")
	req.Data["rules"] = rules
	resp, err := b.HandleRequest(ctx, req)
	if err != nil {
		t.Fatalf("err: %v %#v", err, resp)
	}
	if resp != nil && (resp.IsError() || len(resp.Data) > 0) {
		t.Fatalf("bad: %#v", resp)
	}

	if err := core.identityStore.upsertEntity(ctx, &identity.Entity{
		ID:        "abcd",
		Name:      "abcd",
		BucketKey: "abcd",
	}, nil, false); err != nil {
		t.Fatal(err)
	}

	te := &logical.TokenEntry{
		TTL:         300 * time.Second,
		EntityID:    "abcd",
		Policies:    []string{"default", "foo"},
		NamespaceID: namespace.RootNamespaceID,
	}
	if err := core.tokenStore.create(ctx, te); err != nil {
		t.Fatal(err)
	}
	t.Logf("token id: %s", te.ID)

	if err := core.expiration.RegisterAuth(ctx, te, &logical.Auth{
		LeaseOptions: logical.LeaseOptions{
			TTL: te.TTL,
		},
		ClientToken: te.ID,
		Accessor:    te.Accessor,
		Orphan:      true,
	}); err != nil {
		t.Fatal(err)
	}

	// Check the mount access func
	req = logical.TestRequest(t, logical.ReadOperation, "internal/ui/mounts/kv-v1/baz")
	req.ClientToken = te.ID
	resp, err = b.HandleRequest(ctx, req)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("expected 403, got err: %v", err)
	}

	// Modify policy to pass
	rules = `path "kv-v1/+" { capabilities = ["read"] }`
	req = logical.TestRequest(t, logical.UpdateOperation, "policy/foo")
	req.Data["rules"] = rules
	resp, err = b.HandleRequest(ctx, req)
	if err != nil {
		t.Fatalf("err: %v %#v", err, resp)
	}
	if resp != nil && (resp.IsError() || len(resp.Data) > 0) {
		t.Fatalf("bad: %#v", resp)
	}

	// Check the mount access func again
	req = logical.TestRequest(t, logical.ReadOperation, "internal/ui/mounts/kv-v1/baz")
	req.ClientToken = te.ID
	resp, err = b.HandleRequest(ctx, req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
}

func TestHandlePoliciesPasswordSet(t *testing.T) {
	type testCase struct {
		inputData *framework.FieldData

		storage *logical.InmemStorage

		expectedResp  *logical.Response
		expectErr     bool
		expectedStore map[string]*logical.StorageEntry
	}

	tests := map[string]testCase{
		"missing policy name": {
			inputData: passwordPoliciesFieldData(map[string]interface{}{
				"policy": `length = 20
							rule "charset" {
								charset="abcdefghij"
							}`,
			}),

			storage: new(logical.InmemStorage),

			expectedResp:  nil,
			expectErr:     true,
			expectedStore: map[string]*logical.StorageEntry{},
		},
		"missing policy": {
			inputData: passwordPoliciesFieldData(map[string]interface{}{
				"name": "testpolicy",
			}),

			storage: new(logical.InmemStorage),

			expectedResp:  nil,
			expectErr:     true,
			expectedStore: map[string]*logical.StorageEntry{},
		},
		"garbage policy": {
			inputData: passwordPoliciesFieldData(map[string]interface{}{
				"name":   "testpolicy",
				"policy": "hasdukfhiuashdfoiasjdf",
			}),

			storage: new(logical.InmemStorage),

			expectedResp:  nil,
			expectErr:     true,
			expectedStore: map[string]*logical.StorageEntry{},
		},
		"storage failure": {
			inputData: passwordPoliciesFieldData(map[string]interface{}{
				"name": "testpolicy",
				"policy": "length = 20\n" +
					"rule \"charset\" {\n" +
					"	charset=\"abcdefghij\"\n" +
					"}",
			}),

			storage: new(logical.InmemStorage).FailPut(true),

			expectedResp:  nil,
			expectErr:     true,
			expectedStore: map[string]*logical.StorageEntry{},
		},
		"impossible policy": {
			inputData: passwordPoliciesFieldData(map[string]interface{}{
				"name": "testpolicy",
				"policy": "length = 20\n" +
					"rule \"charset\" {\n" +
					"	charset=\"a\"\n" +
					"	min-chars = 30\n" +
					"}",
			}),

			storage: new(logical.InmemStorage),

			expectedResp:  nil,
			expectErr:     true,
			expectedStore: map[string]*logical.StorageEntry{},
		},
		"not base64 encoded": {
			inputData: passwordPoliciesFieldData(map[string]interface{}{
				"name": "testpolicy",
				"policy": "length = 20\n" +
					"rule \"charset\" {\n" +
					"	charset=\"abcdefghij\"\n" +
					"}",
			}),

			storage: new(logical.InmemStorage),

			expectedResp: &logical.Response{
				Data: map[string]interface{}{
					logical.HTTPContentType: "application/json",
					logical.HTTPStatusCode:  http.StatusNoContent,
				},
			},
			expectErr: false,
			expectedStore: makeStorageMap(storageEntry(t, "testpolicy", "length = 20\n"+
				"rule \"charset\" {\n"+
				"	charset=\"abcdefghij\"\n"+
				"}")),
		},
		"base64 encoded": {
			inputData: passwordPoliciesFieldData(map[string]interface{}{
				"name": "testpolicy",
				"policy": base64Encode(
					"length = 20\n" +
						"rule \"charset\" {\n" +
						"	charset=\"abcdefghij\"\n" +
						"}"),
			}),

			storage: new(logical.InmemStorage),

			expectedResp: &logical.Response{
				Data: map[string]interface{}{
					logical.HTTPContentType: "application/json",
					logical.HTTPStatusCode:  http.StatusNoContent,
				},
			},
			expectErr: false,
			expectedStore: makeStorageMap(storageEntry(t, "testpolicy",
				"length = 20\n"+
					"rule \"charset\" {\n"+
					"	charset=\"abcdefghij\"\n"+
					"}")),
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			req := &logical.Request{
				Storage: test.storage,
			}

			b := &SystemBackend{}

			actualResp, err := b.handlePoliciesPasswordSet(ctx, req, test.inputData)
			if test.expectErr && err == nil {
				t.Fatalf("err expected, got nil")
			}
			if !test.expectErr && err != nil {
				t.Fatalf("no error expected, got: %s", err)
			}
			if !reflect.DeepEqual(actualResp, test.expectedResp) {
				t.Fatalf("Actual response: %#v\nExpected response: %#v", actualResp, test.expectedResp)
			}

			actualStore := LogicalToMap(t, ctx, test.storage)
			if !reflect.DeepEqual(actualStore, test.expectedStore) {
				t.Fatalf("Actual: %#v\nActual: %#v", dereferenceMap(actualStore), dereferenceMap(test.expectedStore))
			}
		})
	}
}

func TestHandlePoliciesPasswordGet(t *testing.T) {
	type testCase struct {
		inputData *framework.FieldData

		storage *logical.InmemStorage

		expectedResp  *logical.Response
		expectErr     bool
		expectedStore map[string]*logical.StorageEntry
	}

	tests := map[string]testCase{
		"missing policy name": {
			inputData: passwordPoliciesFieldData(map[string]interface{}{}),

			storage: new(logical.InmemStorage),

			expectedResp:  nil,
			expectErr:     true,
			expectedStore: map[string]*logical.StorageEntry{},
		},
		"storage error": {
			inputData: passwordPoliciesFieldData(map[string]interface{}{
				"name": "testpolicy",
			}),

			storage: new(logical.InmemStorage).FailGet(true),

			expectedResp:  nil,
			expectErr:     true,
			expectedStore: map[string]*logical.StorageEntry{},
		},
		"missing value": {
			inputData: passwordPoliciesFieldData(map[string]interface{}{
				"name": "testpolicy",
			}),

			storage: new(logical.InmemStorage),

			expectedResp:  nil,
			expectErr:     true,
			expectedStore: map[string]*logical.StorageEntry{},
		},
		"good value": {
			inputData: passwordPoliciesFieldData(map[string]interface{}{
				"name": "testpolicy",
			}),

			storage: makeStorage(t, storageEntry(t, "testpolicy",
				"length = 20\n"+
					"rule \"charset\" {\n"+
					"	charset=\"abcdefghij\"\n"+
					"}")),

			expectedResp: &logical.Response{
				Data: map[string]interface{}{
					"policy": "length = 20\n" +
						"rule \"charset\" {\n" +
						"	charset=\"abcdefghij\"\n" +
						"}",
				},
			},
			expectErr: false,
			expectedStore: makeStorageMap(storageEntry(t, "testpolicy",
				"length = 20\n"+
					"rule \"charset\" {\n"+
					"	charset=\"abcdefghij\"\n"+
					"}")),
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()

			req := &logical.Request{
				Storage: test.storage,
			}

			b := &SystemBackend{}

			actualResp, err := b.handlePoliciesPasswordGet(ctx, req, test.inputData)
			if test.expectErr && err == nil {
				t.Fatalf("err expected, got nil")
			}
			if !test.expectErr && err != nil {
				t.Fatalf("no error expected, got: %s", err)
			}
			if !reflect.DeepEqual(actualResp, test.expectedResp) {
				t.Fatalf("Actual response: %#v\nExpected response: %#v", actualResp, test.expectedResp)
			}

			actualStore := LogicalToMap(t, ctx, test.storage)
			if !reflect.DeepEqual(actualStore, test.expectedStore) {
				t.Fatalf("Actual: %#v\nActual: %#v", dereferenceMap(actualStore), dereferenceMap(test.expectedStore))
			}
		})
	}
}

func TestHandlePoliciesPasswordDelete(t *testing.T) {
	type testCase struct {
		inputData *framework.FieldData

		storage logical.Storage

		expectedResp  *logical.Response
		expectErr     bool
		expectedStore map[string]*logical.StorageEntry
	}

	tests := map[string]testCase{
		"missing policy name": {
			inputData: passwordPoliciesFieldData(map[string]interface{}{}),

			storage: new(logical.InmemStorage),

			expectedResp:  nil,
			expectErr:     true,
			expectedStore: map[string]*logical.StorageEntry{},
		},
		"storage failure": {
			inputData: passwordPoliciesFieldData(map[string]interface{}{
				"name": "testpolicy",
			}),

			storage: new(logical.InmemStorage).FailDelete(true),

			expectedResp:  nil,
			expectErr:     true,
			expectedStore: map[string]*logical.StorageEntry{},
		},
		"successful delete": {
			inputData: passwordPoliciesFieldData(map[string]interface{}{
				"name": "testpolicy",
			}),

			storage: makeStorage(t,
				&logical.StorageEntry{
					Key: getPasswordPolicyKey("testpolicy"),
					Value: toJson(t,
						passwordPolicyConfig{
							HCLPolicy: "length = 18\n" +
								"rule \"charset\" {\n" +
								"	charset=\"ABCDEFGHIJ\"\n" +
								"}",
						}),
				},
				&logical.StorageEntry{
					Key: getPasswordPolicyKey("unrelated_policy"),
					Value: toJson(t,
						passwordPolicyConfig{
							HCLPolicy: "length = 20\n" +
								"rule \"charset\" {\n" +
								"	charset=\"abcdefghij\"\n" +
								"}",
						}),
				},
			),

			expectedResp: nil,
			expectErr:    false,
			expectedStore: makeStorageMap(storageEntry(t, "unrelated_policy",
				"length = 20\n"+
					"rule \"charset\" {\n"+
					"	charset=\"abcdefghij\"\n"+
					"}")),
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()

			req := &logical.Request{
				Storage: test.storage,
			}

			b := &SystemBackend{}

			actualResp, err := b.handlePoliciesPasswordDelete(ctx, req, test.inputData)
			if test.expectErr && err == nil {
				t.Fatalf("err expected, got nil")
			}
			if !test.expectErr && err != nil {
				t.Fatalf("no error expected, got: %s", err)
			}
			if !reflect.DeepEqual(actualResp, test.expectedResp) {
				t.Fatalf("Actual response: %#v\nExpected response: %#v", actualResp, test.expectedResp)
			}

			actualStore := LogicalToMap(t, ctx, test.storage)
			if !reflect.DeepEqual(actualStore, test.expectedStore) {
				t.Fatalf("Actual: %#v\nExpected: %#v", dereferenceMap(actualStore), dereferenceMap(test.expectedStore))
			}
		})
	}
}

func TestHandlePoliciesPasswordList(t *testing.T) {
	type testCase struct {
		storage logical.Storage

		expectErr    bool
		expectedResp *logical.Response
	}

	tests := map[string]testCase{
		"no policies": {
			storage: new(logical.InmemStorage),

			expectedResp: &logical.Response{
				Data: map[string]interface{}{},
			},
		},
		"one policy": {
			storage: makeStorage(t,
				&logical.StorageEntry{
					Key: getPasswordPolicyKey("testpolicy"),
					Value: toJson(t,
						passwordPolicyConfig{
							HCLPolicy: "length = 18\n" +
								"rule \"charset\" {\n" +
								"	charset=\"ABCDEFGHIJ\"\n" +
								"}",
						}),
				},
			),

			expectedResp: &logical.Response{
				Data: map[string]interface{}{
					"keys": []string{"testpolicy"},
				},
			},
		},
		"two policies": {
			storage: makeStorage(t,
				&logical.StorageEntry{
					Key: getPasswordPolicyKey("testpolicy"),
					Value: toJson(t,
						passwordPolicyConfig{
							HCLPolicy: "length = 18\n" +
								"rule \"charset\" {\n" +
								"	charset=\"ABCDEFGHIJ\"\n" +
								"}",
						}),
				},
				&logical.StorageEntry{
					Key: getPasswordPolicyKey("unrelated_policy"),
					Value: toJson(t,
						passwordPolicyConfig{
							HCLPolicy: "length = 20\n" +
								"rule \"charset\" {\n" +
								"	charset=\"abcdefghij\"\n" +
								"}",
						}),
				},
			),

			expectedResp: &logical.Response{
				Data: map[string]interface{}{
					"keys": []string{
						"testpolicy",
						"unrelated_policy",
					},
				},
			},
		},
		"storage failure": {
			storage: new(logical.InmemStorage).FailList(true),

			expectErr: true,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()

			req := &logical.Request{
				Storage: test.storage,
			}

			b := &SystemBackend{}

			actualResp, err := b.handlePoliciesPasswordList(ctx, req, nil)
			if test.expectErr && err == nil {
				t.Fatalf("err expected, got nil")
			}
			if !test.expectErr && err != nil {
				t.Fatalf("no error expected, got: %s", err)
			}
			if !reflect.DeepEqual(actualResp, test.expectedResp) {
				t.Fatalf("Actual response: %#v\nExpected response: %#v", actualResp, test.expectedResp)
			}
		})
	}
}

func TestHandlePoliciesPasswordGenerate(t *testing.T) {
	t.Run("errors", func(t *testing.T) {
		type testCase struct {
			timeout   time.Duration
			inputData *framework.FieldData

			storage *logical.InmemStorage

			expectedResp *logical.Response
			expectErr    bool
		}

		tests := map[string]testCase{
			"missing policy name": {
				inputData: passwordPoliciesFieldData(map[string]interface{}{}),

				storage: new(logical.InmemStorage),

				expectedResp: nil,
				expectErr:    true,
			},
			"storage failure": {
				inputData: passwordPoliciesFieldData(map[string]interface{}{
					"name": "testpolicy",
				}),

				storage: new(logical.InmemStorage).FailGet(true),

				expectedResp: nil,
				expectErr:    true,
			},
			"policy does not exist": {
				inputData: passwordPoliciesFieldData(map[string]interface{}{
					"name": "testpolicy",
				}),

				storage: new(logical.InmemStorage),

				expectedResp: nil,
				expectErr:    true,
			},
			"policy improperly saved": {
				inputData: passwordPoliciesFieldData(map[string]interface{}{
					"name": "testpolicy",
				}),

				storage: makeStorage(t, storageEntry(t, "testpolicy", "badpolicy")),

				expectedResp: nil,
				expectErr:    true,
			},
			"failed to generate": {
				timeout: 0 * time.Second, // Timeout immediately
				inputData: passwordPoliciesFieldData(map[string]interface{}{
					"name": "testpolicy",
				}),

				storage: makeStorage(t, storageEntry(t, "testpolicy",
					"length = 20\n"+
						"rule \"charset\" {\n"+
						"	charset=\"abcdefghij\"\n"+
						"}")),

				expectedResp: nil,
				expectErr:    true,
			},
		}

		for name, test := range tests {
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), test.timeout)
				defer cancel()

				req := &logical.Request{
					Storage: test.storage,
				}

				b := &SystemBackend{}

				actualResp, err := b.handlePoliciesPasswordGenerate(ctx, req, test.inputData)
				if test.expectErr && err == nil {
					t.Fatalf("err expected, got nil")
				}
				if !test.expectErr && err != nil {
					t.Fatalf("no error expected, got: %s", err)
				}
				if !reflect.DeepEqual(actualResp, test.expectedResp) {
					t.Fatalf("Actual response: %#v\nExpected response: %#v", actualResp, test.expectedResp)
				}
			})
		}
	})

	t.Run("success", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		policyEntry := storageEntry(t, "testpolicy",
			"length = 20\n"+
				"rule \"charset\" {\n"+
				"	charset=\"abcdefghij\"\n"+
				"}")
		storage := makeStorage(t, policyEntry)

		inputData := passwordPoliciesFieldData(map[string]interface{}{
			"name": "testpolicy",
		})

		expectedResp := &logical.Response{
			Data: map[string]interface{}{
				// Doesn't include the password as that's pulled out and compared separately
			},
		}

		// Password assertions
		expectedPassLen := 20
		rules := []random.Rule{
			random.CharsetRule{
				Charset:  []rune("abcdefghij"),
				MinChars: expectedPassLen,
			},
		}

		// Run the test a bunch of times to help ensure we don't have flaky behavior
		for i := 0; i < 1000; i++ {
			req := &logical.Request{
				Storage: storage,
			}

			b := &SystemBackend{}

			actualResp, err := b.handlePoliciesPasswordGenerate(ctx, req, inputData)
			if err != nil {
				t.Fatalf("no error expected, got: %s", err)
			}

			assertTrue(t, actualResp != nil, "response is nil")
			assertTrue(t, actualResp.Data != nil, "expected data, got nil")
			assertHasKey(t, actualResp.Data, "password", "password key not found in data")
			assertIsString(t, actualResp.Data["password"], "password key should have a string value")
			password := actualResp.Data["password"].(string)

			// Delete the password so the rest of the response can be compared
			delete(actualResp.Data, "password")
			assertTrue(t, reflect.DeepEqual(actualResp, expectedResp), "Actual response: %#v\nExpected response: %#v", actualResp, expectedResp)

			// Check to make sure the password is correctly formatted
			passwordLength := len([]rune(password))
			if passwordLength != expectedPassLen {
				t.Fatalf("password is %d characters but should be %d", passwordLength, expectedPassLen)
			}

			for _, rule := range rules {
				if !rule.Pass([]rune(password)) {
					t.Fatalf("password %s does not have the correct characters", password)
				}
			}
		}
	})
}

func assertTrue(t *testing.T, pass bool, f string, vals ...interface{}) {
	t.Helper()
	if !pass {
		t.Fatalf(f, vals...)
	}
}

func assertHasKey(t *testing.T, m map[string]interface{}, key string, f string, vals ...interface{}) {
	t.Helper()
	_, exists := m[key]
	if !exists {
		t.Fatalf(f, vals...)
	}
}

func assertIsString(t *testing.T, val interface{}, f string, vals ...interface{}) {
	t.Helper()
	_, ok := val.(string)
	if !ok {
		t.Fatalf(f, vals...)
	}
}

func passwordPoliciesFieldData(raw map[string]interface{}) *framework.FieldData {
	return &framework.FieldData{
		Raw: raw,
		Schema: map[string]*framework.FieldSchema{
			"name": {
				Type:        framework.TypeString,
				Description: "The name of the password policy.",
			},
			"policy": {
				Type:        framework.TypeString,
				Description: "The password policy",
			},
		},
	}
}

func base64Encode(data string) string {
	return base64.StdEncoding.EncodeToString([]byte(data))
}

func toJson(t *testing.T, val interface{}) []byte {
	t.Helper()

	b, err := jsonutil.EncodeJSON(val)
	if err != nil {
		t.Fatalf("Unable to marshal to JSON: %s", err)
	}
	return b
}

func storageEntry(t *testing.T, key string, policy string) *logical.StorageEntry {
	return &logical.StorageEntry{
		Key: getPasswordPolicyKey(key),
		Value: toJson(t, passwordPolicyConfig{
			HCLPolicy: policy,
		}),
	}
}

func makeStorageMap(entries ...*logical.StorageEntry) map[string]*logical.StorageEntry {
	m := map[string]*logical.StorageEntry{}
	for _, entry := range entries {
		m[entry.Key] = entry
	}
	return m
}

func dereferenceMap(store map[string]*logical.StorageEntry) map[string]interface{} {
	m := map[string]interface{}{}

	for k, v := range store {
		m[k] = map[string]string{
			"Key":   v.Key,
			"Value": string(v.Value),
		}
	}
	return m
}

type walkFunc func(*logical.StorageEntry) error

// WalkLogicalStorage applies the provided walkFunc against each entry in the logical storage.
// This operates as a breadth first search.
// TODO: Figure out a place for this to live permanently. This is generic and should be in a helper package somewhere.
// At the time of writing, none of these locations work due to import cycles:
// - vault/helper/testhelpers
// - vault/helper/testhelpers/logical
// - vault/helper/testhelpers/teststorage
func WalkLogicalStorage(ctx context.Context, store logical.Storage, walker walkFunc) (err error) {
	if store == nil {
		return fmt.Errorf("no storage provided")
	}
	if walker == nil {
		return fmt.Errorf("no walk function provided")
	}

	keys, err := store.List(ctx, "")
	if err != nil {
		return fmt.Errorf("unable to list root keys: %w", err)
	}

	// Non-recursive breadth-first search through all keys
	for i := 0; i < len(keys); i++ {
		key := keys[i]

		entry, err := store.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("unable to retrieve key at [%s]: %w", key, err)
		}
		if entry != nil {
			err = walker(entry)
			if err != nil {
				return err
			}
		}

		if strings.HasSuffix(key, "/") {
			// Directory
			subkeys, err := store.List(ctx, key)
			if err != nil {
				return fmt.Errorf("unable to list keys at [%s]: %w", key, err)
			}

			// Append the sub-keys to the keys slice so it searches into the sub-directory
			for _, subkey := range subkeys {
				// Avoids infinite loop if the subkey is empty which then repeats indefinitely
				if subkey == "" {
					continue
				}
				subkey = fmt.Sprintf("%s%s", key, subkey)
				keys = append(keys, subkey)
			}
		}
	}
	return nil
}

// LogicalToMap retrieves all entries in the store and returns them as a map of key -> StorageEntry
func LogicalToMap(t *testing.T, ctx context.Context, store logical.Storage) (data map[string]*logical.StorageEntry) {
	data = map[string]*logical.StorageEntry{}
	f := func(entry *logical.StorageEntry) error {
		data[entry.Key] = entry
		return nil
	}

	err := WalkLogicalStorage(ctx, store, f)
	if err != nil {
		t.Fatalf("Unable to walk the storage: %s", err)
	}
	return data
}

// Ensure the WalkLogicalStorage function works
func TestWalkLogicalStorage(t *testing.T) {
	type testCase struct {
		entries []*logical.StorageEntry
	}

	tests := map[string]testCase{
		"no entries": {
			entries: []*logical.StorageEntry{},
		},
		"one entry": {
			entries: []*logical.StorageEntry{
				{
					Key: "root",
				},
			},
		},
		"many entries": {
			entries: []*logical.StorageEntry{
				// Alphabetical, breadth-first
				{Key: "bar"},
				{Key: "foo"},
				{Key: "bar/sub-bar1"},
				{Key: "bar/sub-bar2"},
				{Key: "foo/sub-foo1"},
				{Key: "foo/sub-foo2"},
				{Key: "foo/sub-foo3"},
				{Key: "bar/sub-bar1/sub-sub-bar1"},
				{Key: "bar/sub-bar1/sub-sub-bar2"},
				{Key: "bar/sub-bar2/sub-sub-bar1"},
				{Key: "foo/sub-foo1/sub-sub-foo1"},
				{Key: "foo/sub-foo2/sub-sub-foo1"},
				{Key: "foo/sub-foo3/sub-sub-foo1"},
				{Key: "foo/sub-foo3/sub-sub-foo2"},
			},
		},
		"sub key without root key": {
			entries: []*logical.StorageEntry{
				{Key: "foo/bar/baz"},
			},
		},
		"key with trailing slash": {
			entries: []*logical.StorageEntry{
				{Key: "foo/"},
			},
		},
		"double slash": {
			entries: []*logical.StorageEntry{
				{Key: "foo//"},
				{Key: "foo//bar"},
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			store := makeStorage(t, test.entries...)

			actualEntries := []*logical.StorageEntry{}
			f := func(entry *logical.StorageEntry) error {
				actualEntries = append(actualEntries, entry)
				return nil
			}

			err := WalkLogicalStorage(ctx, store, f)
			if err != nil {
				t.Fatalf("Failed to walk storage: %s", err)
			}

			if !reflect.DeepEqual(actualEntries, test.entries) {
				t.Fatalf("Actual: %#v\nExpected: %#v", actualEntries, test.entries)
			}
		})
	}
}

func makeStorage(t *testing.T, entries ...*logical.StorageEntry) *logical.InmemStorage {
	t.Helper()

	ctx := context.Background()

	store := new(logical.InmemStorage)

	for _, entry := range entries {
		err := store.Put(ctx, entry)
		if err != nil {
			t.Fatalf("Unable to load test storage: %s", err)
		}
	}

	return store
}

func leaseLimitFieldData(limit string) *framework.FieldData {
	raw := make(map[string]interface{})
	raw["limit"] = limit
	return &framework.FieldData{
		Raw: raw,
		Schema: map[string]*framework.FieldSchema{
			"limit": {
				Type:        framework.TypeString,
				Default:     "",
				Description: "limit return results",
			},
		},
	}
}

func TestProcessLimit(t *testing.T) {
	testCases := []struct {
		d               *framework.FieldData
		expectReturnAll bool
		expectLimit     int
		expectErr       bool
	}{
		{
			d:               leaseLimitFieldData("500"),
			expectReturnAll: false,
			expectLimit:     500,
			expectErr:       false,
		},
		{
			d:               leaseLimitFieldData(""),
			expectReturnAll: false,
			expectLimit:     MaxIrrevocableLeasesToReturn,
			expectErr:       false,
		},
		{
			d:               leaseLimitFieldData("none"),
			expectReturnAll: true,
			expectLimit:     10000,
			expectErr:       false,
		},
		{
			d:               leaseLimitFieldData("NoNe"),
			expectReturnAll: true,
			expectLimit:     10000,
			expectErr:       false,
		},
		{
			d:               leaseLimitFieldData("hello_world"),
			expectReturnAll: false,
			expectLimit:     0,
			expectErr:       true,
		},
		{
			d:               leaseLimitFieldData("0"),
			expectReturnAll: false,
			expectLimit:     0,
			expectErr:       true,
		},
		{
			d:               leaseLimitFieldData("-1"),
			expectReturnAll: false,
			expectLimit:     0,
			expectErr:       true,
		},
	}

	for i, tc := range testCases {
		returnAll, limit, err := processLimit(tc.d)

		if returnAll != tc.expectReturnAll {
			t.Errorf("bad return all for test case %d. expected %t, got %t", i, tc.expectReturnAll, returnAll)
		}
		if limit != tc.expectLimit {
			t.Errorf("bad limit for test case %d. expected %d, got %d", i, tc.expectLimit, limit)
		}

		haveErr := err != nil
		if haveErr != tc.expectErr {
			t.Errorf("bad error status for test case %d. expected error: %t, got error: %t", i, tc.expectErr, haveErr)
			if err != nil {
				t.Errorf("error was: %v", err)
			}
		}
	}
}
