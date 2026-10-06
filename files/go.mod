module github.com/Syluxso/byz-platform/files

go 1.26.0

require github.com/Syluxso/byz-platform v0.0.0

require (
	github.com/MicahParks/jwkset v0.11.0 // indirect
	github.com/MicahParks/keyfunc/v3 v3.8.0 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	golang.org/x/time v0.9.0 // indirect
)

// Local development: resolve core from this repo until it is tagged.
replace github.com/Syluxso/byz-platform => ../
