package token

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"gopkg.in/yaml.v3"
)

// K0sRole selects the k0s join flavour: workers talk to the Kubernetes API
// (:6443), controllers to the k0s join API (:9443).
type K0sRole string

const (
	K0sWorker     K0sRole = "worker"
	K0sController K0sRole = "controller"

	k0sWorkerPort     = 6443
	k0sControllerPort = 9443

	// User names k0s puts into the join kubeconfig; `k0s worker` and
	// `k0s controller` use them to tell the token flavours apart
	// (k0s pkg/token/kubeconfig.go, WorkerTokenAuthName/ControllerTokenAuthName).
	k0sWorkerUser     = "kubelet-bootstrap"
	k0sControllerUser = "controller-bootstrap"
	k0sContextName    = "k0s"

	// SecretType is the Kubernetes bootstrap token Secret type.
	SecretType = "bootstrap.kubernetes.io/token"

	k0sMaxDecoded = 1 << 20
)

// K0sJoin is the content of a decoded k0s join token.
type K0sJoin struct {
	Role   K0sRole
	Server string
	CACert []byte
	Token  string
	User   string
}

type k0sKubeconfig struct {
	APIVersion     string       `yaml:"apiVersion"`
	Kind           string       `yaml:"kind"`
	Clusters       []k0sCluster `yaml:"clusters"`
	Contexts       []k0sContext `yaml:"contexts"`
	CurrentContext string       `yaml:"current-context"`
	Users          []k0sUser    `yaml:"users"`
}

type k0sCluster struct {
	Name    string `yaml:"name"`
	Cluster struct {
		Server string `yaml:"server"`
		CAData string `yaml:"certificate-authority-data"`
	} `yaml:"cluster"`
}

type k0sContext struct {
	Name    string `yaml:"name"`
	Context struct {
		Cluster string `yaml:"cluster"`
		User    string `yaml:"user"`
	} `yaml:"context"`
}

type k0sUser struct {
	Name string `yaml:"name"`
	User struct {
		Token string `yaml:"token"`
	} `yaml:"user"`
}

// EncodeK0s builds the join token `k0s token pre-shared --role <role>
// --cert ca.crt --url https://<endpoint>` would: a kubeconfig whose
// cluster carries the CA and whose user carries the bootstrap token,
// gzip-compressed (BestCompression) and base64-encoded (k0s
// pkg/token/joinencode.go, pkg/token/kubeconfig.go GenerateKubeconfig).
// endpoint is host[:port]; without a port workers get :6443 and
// controllers :9443.
func EncodeK0s(role K0sRole, endpoint string, caCert []byte, bootstrapToken string) (string, error) {
	user, port, err := k0sRoleParams(role)
	if err != nil {
		return "", err
	}
	if _, _, err := Split(bootstrapToken); err != nil {
		return "", err
	}
	if len(bytes.TrimSpace(caCert)) == 0 {
		return "", errors.New("k0s token: CA certificate is empty")
	}
	host, p, err := config.ParseHostPort(endpoint)
	if err != nil {
		return "", fmt.Errorf("k0s token: %w", err)
	}
	if p != 0 {
		port = p
	}
	var cluster k0sCluster
	cluster.Name = k0sContextName
	cluster.Cluster.Server = "https://" + net.JoinHostPort(host, strconv.Itoa(port))
	cluster.Cluster.CAData = base64.StdEncoding.EncodeToString(caCert)
	var ctx k0sContext
	ctx.Name = k0sContextName
	ctx.Context.Cluster = k0sContextName
	ctx.Context.User = user
	var u k0sUser
	u.Name = user
	u.User.Token = bootstrapToken
	kubeconfig, err := yaml.Marshal(k0sKubeconfig{
		APIVersion:     "v1",
		Kind:           "Config",
		Clusters:       []k0sCluster{cluster},
		Contexts:       []k0sContext{ctx},
		CurrentContext: k0sContextName,
		Users:          []k0sUser{u},
	})
	if err != nil {
		return "", fmt.Errorf("k0s token: %w", err)
	}
	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return "", err
	}
	if _, err := gz.Write(kubeconfig); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// DecodeK0s reverses EncodeK0s (k0s pkg/token/joindecode.go
// DecodeJoinToken) and returns the kubeconfig bytes.
func DecodeK0s(token string) ([]byte, error) {
	gzData, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("k0s token: base64: %w", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(gzData))
	if err != nil {
		return nil, fmt.Errorf("k0s token: gzip: %w", err)
	}
	defer config.CloseQuietly(gz, "k0s token")
	out, err := io.ReadAll(io.LimitReader(gz, k0sMaxDecoded))
	if err != nil {
		return nil, fmt.Errorf("k0s token: gzip: %w", err)
	}
	return out, nil
}

// ParseK0s decodes a k0s join token and extracts the fields a node acts on.
func ParseK0s(token string) (K0sJoin, error) {
	kubeconfig, err := DecodeK0s(token)
	if err != nil {
		return K0sJoin{}, err
	}
	var kc k0sKubeconfig
	if err := yaml.Unmarshal(kubeconfig, &kc); err != nil {
		return K0sJoin{}, fmt.Errorf("k0s token: kubeconfig: %w", err)
	}
	if len(kc.Clusters) != 1 || len(kc.Users) != 1 || len(kc.Contexts) != 1 {
		return K0sJoin{}, errors.New("k0s token: kubeconfig must hold exactly one cluster, user and context")
	}
	ca, err := base64.StdEncoding.DecodeString(kc.Clusters[0].Cluster.CAData)
	if err != nil {
		return K0sJoin{}, fmt.Errorf("k0s token: certificate-authority-data: %w", err)
	}
	j := K0sJoin{Server: kc.Clusters[0].Cluster.Server, CACert: ca, Token: kc.Users[0].User.Token, User: kc.Contexts[0].Context.User}
	switch j.User {
	case k0sWorkerUser:
		j.Role = K0sWorker
	case k0sControllerUser:
		j.Role = K0sController
	default:
		return K0sJoin{}, fmt.Errorf("k0s token: unknown user %q", j.User)
	}
	return j, nil
}

func k0sRoleParams(role K0sRole) (user string, port int, err error) {
	switch role {
	case K0sWorker:
		return k0sWorkerUser, k0sWorkerPort, nil
	case K0sController:
		return k0sControllerUser, k0sControllerPort, nil
	}
	return "", 0, fmt.Errorf("k0s token: unsupported role %q; supported roles are %q and %q", role, K0sController, K0sWorker)
}

type secretManifest struct {
	APIVersion string            `yaml:"apiVersion"`
	Kind       string            `yaml:"kind"`
	Metadata   map[string]string `yaml:"metadata"`
	Type       string            `yaml:"type"`
	Data       map[string]string `yaml:"data"`
}

// K0sBootstrapSecret renders the kube-system Secret that makes a k0s
// pre-shared token valid, in exactly the shape `k0s token pre-shared`
// writes (k0s v1.36.4+k0s.1 pkg/token/manager.go RandomBootstrapSecret,
// lines 60-92, through kubeadm's BootstrapTokenToSecret in
// cmd/kubeadm/app/apis/bootstraptoken/v1/utils.go lines 99-145; verified
// against a real `k0s token pre-shared` run, see testdata/k0s-*-secret.yaml):
//
//   - name bootstrap-token-<id>, namespace kube-system, type
//     bootstrap.kubernetes.io/token, values base64 in `data`;
//   - token-id, token-secret, description, expiration (RFC 3339, UTC);
//   - worker: usage-bootstrap-authentication=true;
//   - controller: usage-bootstrap-controller-join=true plus the legacy
//     usage-controller-join=true;
//   - no auth-extra-groups: k0s relies on the implicit
//     system:bootstrappers group, unlike kubeadm's default-node-token.
func K0sBootstrapSecret(role K0sRole, bootstrapToken string, expires time.Time) ([]byte, error) {
	id, secret, err := Split(bootstrapToken)
	if err != nil {
		return nil, err
	}
	data := map[string]string{
		"token-id":     id,
		"token-secret": secret,
		"expiration":   expires.UTC().Format(time.RFC3339),
	}
	switch role {
	case K0sWorker:
		data["description"] = "Worker bootstrap token generated by k0s"
		data["usage-bootstrap-authentication"] = "true"
	case K0sController:
		data["description"] = "Controller bootstrap token generated by k0s"
		data["usage-bootstrap-controller-join"] = "true"
		data["usage-controller-join"] = "true"
	default:
		_, _, err := k0sRoleParams(role)
		return nil, err
	}
	encoded := make(map[string]string, len(data))
	for k, v := range data {
		encoded[k] = base64.StdEncoding.EncodeToString([]byte(v))
	}
	return yaml.Marshal(secretManifest{
		APIVersion: "v1",
		Kind:       "Secret",
		Metadata:   map[string]string{"name": "bootstrap-token-" + id, "namespace": "kube-system"},
		Type:       SecretType,
		Data:       encoded,
	})
}
