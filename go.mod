module github.com/jeefy/booty

go 1.16

replace (
	github.com/flatcar-linux/container-linux-config-transpiler => github.com/flatcar-linux/container-linux-config-transpiler v0.9.2
	github.com/pin/tftp => github.com/pin/tftp v0.0.0-20210809155059-0161c5dd2e96
)

require (
	github.com/buger/jsonparser v1.1.1
	github.com/coreos/butane v0.19.0
	github.com/coreos/ignition/v2 v2.17.0
	github.com/go-co-op/gocron v1.11.0
	github.com/google/go-containerregistry v0.17.0
	github.com/j-keck/arping v1.0.3
	github.com/joho/godotenv v1.4.0
	github.com/pin/tftp v2.2.0+incompatible
	github.com/spf13/cobra v1.7.0
	github.com/spf13/viper v1.10.1
)
