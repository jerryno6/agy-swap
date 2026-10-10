# Agent Guidelines

- **Unit Tests**: Tuyệt đối KHÔNG viết thêm bất kỳ unit test (`*_test.go`) mới nào. Chỉ bảo trì và kiểm thử các tính năng / UI cũ. Kiểm tra bằng `go test ./...`.
- **Build & Deploy**: Cài đặt bằng `make install BUILD_ID=release` (đích đến: `~/.local/bin/agy-swap`).
- **Windows**: KHÔNG dùng `make install` (Makefile cần POSIX shell và build ra `agy-swap` không có `.exe`). Dùng `pwsh -File scripts/install-windows.ps1` (thêm `-SkipTests` nếu đã test): đọc `VERSION` từ Makefile, build cùng ldflags với `make build`, cài vào DUY NHẤT `%LOCALAPPDATA%\Programs\agy-swap\agy-swap.exe` (cùng chỗ với `install.ps1` và `agy-swap update`), giữ bản cũ thành `agy-swap.exe.<version>.bak`, và cảnh báo nếu còn bản agy-swap khác trong PATH (không tự xoá). Không cài thêm vào `~/.local/bin`. Chi tiết ở `docs/development.md` mục Windows.
- **Version**: Không tự ý tăng version trừ khi có yêu cầu rõ ràng.