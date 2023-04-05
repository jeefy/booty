module github.com/jeefy/booty

go 1.16

replace (
	github.com/flatcar-linux/container-linux-config-transpiler => github.com/flatcar-linux/container-linux-config-transpiler v0.9.2
	github.com/pin/tftp => github.com/pin/tftp v0.0.0-20210809155059-0161c5dd2e96
)

require (
	github.com/flatcar-linux/container-linux-config-transpiler v0.9.2
	github.com/go-co-op/gocron v1.11.0
	github.com/j-keck/arping v1.0.3
	github.com/joho/godotenv v1.4.0
	github.com/pin/tftp v2.2.0+incompatible
	github.com/spf13/cobra v1.3.0
	github.com/spf13/viper v1.10.1
	golang.org/x/net v0.0.0-20220127200216-cd36cc0744dd // indirect
	golang.org/x/sys v0.0.0-20220209214540-3681064d5158 // indirect
)
