package actuator

import "github.com/jeefy/booty/pkg/kubeadm"

func kubeadmConfig(server string) kubeadm.KubeConfig {
	return kubeadm.KubeConfig{APIServer: server, Token: "x"}
}
