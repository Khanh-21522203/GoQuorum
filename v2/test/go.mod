module goquorum.io/v2/test

go 1.25.0

require (
	goquorum.io/v2/client v0.0.0
	goquorum.io/v2/contracts v0.0.0
	goquorum.io/v2/engine v0.0.0
	goquorum.io/v2/infra v0.0.0
	goquorum.io/v2/server v0.0.0
)

replace goquorum.io/v2/client => ../client

replace goquorum.io/v2/contracts => ../contracts

replace goquorum.io/v2/engine => ../engine

replace goquorum.io/v2/gateway => ../gateway

replace goquorum.io/v2/infra => ../infra

replace goquorum.io/v2/server => ../server
