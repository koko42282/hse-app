module usbai

go 1.24.1

toolchain go1.24.7

require (
	github.com/extrame/xls v0.0.1
	github.com/ledongthuc/pdf v0.0.0-20260907135840-6c8c28e0e8a0
)

require github.com/extrame/ole2 v0.0.0-20160812065207-d69429661ad7 // indirect

replace github.com/ledongthuc/pdf => ./third_party/pdf
