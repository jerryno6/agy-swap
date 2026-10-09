# Agent Guidelines

- **Unit Tests**: Tuyệt đối KHÔNG viết thêm bất kỳ unit test (`*_test.go`) mới nào. Chỉ bảo trì và kiểm thử các tính năng / UI cũ. Kiểm tra bằng `go test ./...`.
- **Build & Deploy**: Cài đặt bằng `make install BUILD_ID=release` (đích đến: `~/.local/bin/agy-swap`).
- **Version**: Không tự ý tăng version trừ khi có yêu cầu rõ ràng.