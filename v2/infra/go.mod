module goquorum.io/v2/infra

go 1.25.0

require (
	github.com/iceber/iouring-go v0.0.0-20230403020409-002cfd2e2a90
	gopkg.in/yaml.v3 v3.0.1
	goquorum.io/v2/contracts v0.0.0
	goquorum.io/v2/engine v0.0.0
)

require golang.org/x/sys v0.0.0-20200923182605-d9f96fdee20d

replace goquorum.io/v2/contracts => ../contracts

replace goquorum.io/v2/engine => ../engine

replace github.com/iceber/iouring-go => ../vendor/iouring-go
