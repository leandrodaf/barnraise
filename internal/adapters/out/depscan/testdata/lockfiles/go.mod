module example.com/shop

go 1.22.1

toolchain go1.22.5

require github.com/spf13/cobra v1.8.0 // direct

require (
	golang.org/x/net v0.7.0 // indirect
	golang.org/x/text v0.3.5
)

replace example.com/old => ../old
