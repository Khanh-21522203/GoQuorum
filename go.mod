module goquorum.io

go 1.25.0

require (
	github.com/cespare/xxhash/v2 v2.3.0
	github.com/cockroachdb/pebble v1.1.5
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.27.7
	github.com/prometheus/client_golang v1.15.0
	github.com/prometheus/common v0.42.0
	golang.org/x/time v0.15.0
	google.golang.org/genproto v0.0.0-20260523011958-0a33c5d7ca68 // indirect — pins monolith past split to override cockroachdb/errors@v1.11.3
	google.golang.org/grpc v1.81.1
	gopkg.in/yaml.v3 v3.0.1
	goquorum.io/api v0.0.0
	goquorum.io/client v0.0.0-00010101000000-000000000000
)

require (
	github.com/DataDog/zstd v1.5.2 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cockroachdb/errors v1.11.3 // indirect
	github.com/cockroachdb/fifo v0.0.0-20240606204812-0bbfbd93a7ce // indirect
	github.com/cockroachdb/logtags v0.0.0-20230118201751-21c54148d20b // indirect
	github.com/cockroachdb/redact v1.1.5 // indirect
	github.com/cockroachdb/tokenbucket v0.0.0-20230807174530-cc333fc44b06 // indirect
	github.com/getsentry/sentry-go v0.27.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/golang/snappy v0.0.4 // indirect
	github.com/klauspost/compress v1.16.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.4 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/prometheus/client_model v0.3.0 // indirect
	github.com/prometheus/procfs v0.9.0 // indirect
	github.com/rogpeppe/go-internal v1.9.0 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	golang.org/x/exp v0.0.0-20230626212559-97b1e661b5df // indirect
	golang.org/x/net v0.52.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/text v0.35.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260511170946-3700d4141b60 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260511170946-3700d4141b60 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace goquorum.io/api => ./api

replace goquorum.io/client => ./client
