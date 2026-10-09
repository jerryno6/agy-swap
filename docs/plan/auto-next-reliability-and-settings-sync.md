# Kế hoạch sửa độ tin cậy của auto-next và đồng bộ settings

Ngày: 2026-10-09
Trạng thái: Plan để agent triển khai; chưa implement.
Phạm vi review: repo agy-swap, các luồng next, auto-next trong TUI và ghi active-session credential.

## 1. Quyết định và phạm vi đã chốt với user

- Mục tiêu auto-next: giữ tài khoản active khi còn khỏe; chuyển khi quota sắp hết để công việc tiếp tục.
- Giữ nguyên hành vi lựa chọn hiện tại của lệnh next và policy balanced/sticky/round-robin. Việc next chọn lại active không nằm trong phạm vi sửa lần này theo phản hồi của user.
- Auto-next chỉ hoạt động khi TUI đang mở. Không thêm daemon, service hoặc background process độc lập.
- Sửa fallback của auto-next: một ứng viên có quota tốt nhưng không apply được không được chặn các ứng viên khỏe còn lại.
- Sửa đồng bộ settings: thay đổi từ CLI phải được TUI đang mở nhận diện, gồm bật/tắt auto-next và interval.
- Cập nhật README: refresh mặc định 300 giây; tuổi tối đa snapshot auto-next 15 phút.
- Giữ nguyên ngưỡng và toán tử hiện tại. User mới yêu cầu giải thích, chưa yêu cầu đổi sang <=.
- Không tự tăng version; mặc định để thay đổi unstaged. Không tạo file *_test.go mới theo AGENTS.md.

## 2. Ngưỡng hiện tại và ý nghĩa

Điều kiện kích hoạt:

```text
(has5h AND min5h < 0.20) OR (hasWeekly AND minWeekly < 0.15)
```

| Quota 5h | Quota weekly | Auto-next |
| --- | --- | --- |
| 20% | 15% | Không kích hoạt |
| 19.9% | 80% | Kích hoạt do 5h |
| 80% | 14.9% | Kích hoạt do weekly |
| 21% | 16% | Không kích hoạt |
| Không có | 14.9% | Kích hoạt do weekly |
| Không có | Không có | Không kích hoạt |

- Dùng giá trị fraction thực, không dùng phần trăm đã làm tròn trên UI. Ví dụ 19.96% có thể được hiển thị thành 20% nhưng vẫn nhỏ hơn 20% và kích hoạt.
- Minimum được lấy trên các bucket cùng cửa sổ trong các quota group. Không thay đổi semantics lọc family/threshold trong task này.
- Snapshot phải hợp lệ, không ở tương lai và không quá 15 phút; active account không được có lỗi refresh của lượt đang xét.
- Quota đúng bằng ngưỡng chưa kích hoạt. Nếu user muốn chuyển ngay tại 20% hoặc 15%, cần một yêu cầu đổi hành vi riêng.

## 3. Hiện trạng và vị trí thay đổi

| Vị trí | Hiện trạng | Thay đổi dự kiến |
| --- | --- | --- |
| internal/app/tui_actions.go: handleAutoNext | Chọn một ứng viên, lỗi đọc token/apply thì return ngay | Thử ứng viên kế tiếp có giới hạn và chỉ khi phiên cũ an toàn |
| internal/app/auto_next.go: SelectAutoNextCandidateWithOptions | Lọc và xếp hạng quota; chưa xét khả năng publish credential | Tái sử dụng policy, thêm cơ chế loại ứng viên đã thử trong lượt |
| internal/app/credentials.go: applyUnlocked / writeOAuthFiles | Trả bool; bỏ qua kết quả một số rollback | Trả nguyên nhân lỗi và trạng thái an toàn để retry qua API nội bộ |
| internal/app/credential_windows.go: windowsSessionCredential | Compact JSON và từ chối payload > 2560 byte | Giữ giới hạn, truyền nguyên nhân lỗi lên caller |
| internal/app/tui.go: ticker và tuiAccountsEvent | Refresh quota không reload settings; ticker giữ interval cũ | Đồng bộ settings định kỳ và tại thời điểm quyết định switch |
| internal/app/settings.go: LoadSettings / revision | Đã có normalize, revision và khóa settings | Tái sử dụng; tránh thêm cache/quy tắc normalize khác |
| README.md: Auto-next in the terminal UI | Ghi 60 giây và snapshot >2 phút | Sửa thành 300 giây / 15 phút và giải thích strict boundary |
| internal/app/auto_next.go: comments | Một số comment vẫn ghi 2 phút | Đồng bộ comment với AutoNextMaxQuotaAge |

Số dòng có thể đổi khi implement; dùng tên hàm làm mốc chính.

## 4. Bước A — Làm rõ kết quả transaction credential

Phải làm trước vòng fallback để không retry trên phiên đang hỏng.

### A1. API nội bộ có lỗi cụ thể

- Bổ sung một API nội bộ trả error/kết quả có cấu trúc cho apply; giữ wrapper bool nếu cần để hạn chế tác động caller và kiểm thử hiện có.
- Phân biệt: token invalid, identity mismatch, PrepareSession thất bại, secure-store write thất bại, OAuth-file write thất bại và rollback thất bại.
- Không gộp lỗi Windows payload quá lớn thành thông báo chung. Giữ errWindowsCredentialTooLarge để errors.Is có thể phân loại nếu có wrapping.
- Manual switch/next có thể dùng nguyên nhân lỗi mới để chẩn đoán, nhưng không thay đổi policy chọn tài khoản hoặc tự thêm fallback cho lệnh next.
- Không đưa token, JSON credential, refresh token hoặc JWT vào error/toast/log.

### A2. Contract để cho phép thử ứng viên khác

- Thành công: secure store và các OAuth files đã được publish theo contract hiện có; caller ghi history đúng một lần.
- Thất bại trước mutation: an toàn để thử ứng viên khác, sau khi recheck identity.
- Thất bại sau mutation: chỉ an toàn để thử tiếp khi rollback đã hoàn tất và xác minh trạng thái trở về baseline.
- Rollback lỗi hoặc không xác định được trạng thái: dừng toàn bộ lượt auto-next và báo lỗi có thể hành động.
- Giữ ngoại lệ fallback secure-store theo platform hiện có; không mở rộng hoặc siết policy Windows/Linux/macOS ngoài task.

### A3. Baseline và rollback

- Trong SessionLock, giữ baseline secure credential và snapshot đầy đủ của OAuthToken, OAuthCredentials, GoogleAccounts trước mutation.
- Snapshot phải phân biệt file chưa tồn tại và file rỗng; internal/store.FileSnapshot đã hỗ trợ nil cho file chưa tồn tại.
- Kiểm tra kết quả restoreFiles và phục hồi secure credential; không bỏ qua lỗi rollback rồi đánh dấu retry-safe.
- Sau rollback, xác minh cả ba file và secure credential theo baseline. Chỉ so sánh Current()/OAuthToken() là chưa đủ: OAuthCredentials hoặc GoogleAccounts vẫn có thể sai.
- Nếu credential backend không phân biệt được read error với empty value, chỉ kết luận retry-safe khi có đủ bằng chứng. Không coi mọi giá trị rỗng là rollback thành công.
- Không persist baseline hoặc ghi credential vào báo cáo. Chỉ giữ trong bộ nhớ transaction.

## 5. Bước B — Fallback auto-next có giới hạn

### B1. Lựa chọn ứng viên

- Giữ nguyên các bộ lọc: loại active, lỗi quota/auth, cooldown, snapshot stale/future, quota dưới ngưỡng, minRemaining và preferFamily.
- Giữ nguyên thứ tự policy, tie-break và reserve semantics của selector hiện có.
- Tạo tập attemptedEmails cục bộ cho mỗi lượt refresh; không sửa Accounts hoặc quotaErrors gốc để biểu diễn lỗi apply.
- Phương án ít tác động: bổ sung excludedEmails vào selector nội bộ và giữ các entry point hiện có. Hoặc tách hàm trả danh sách ứng viên đã xếp hạng rồi cho selector hiện tại lấy phần tử đầu.
- Không nhân bản thuật toán ranking sang TUI. Chọn một phương án và dùng chung nguồn policy.
- Mỗi email được thử tối đa một lần trong một lượt; tổng số lần thử không vượt số tài khoản.
- Không thêm cooldown lỗi apply được persist qua các lượt trong task này. Lượt sau có thể thử lại ứng viên từng lỗi, nhưng vẫn phải tiến tới các ứng viên còn lại nếu lỗi lặp lại.

### B2. Trình tự xử lý

1. Kiểm tra demo, revision, browse mode/job/form, settings mới nhất và lỗi store/storage.
2. Xác định active account; xác nhận quota active đủ mới và xuống dưới ngưỡng.
3. Chọn ứng viên tốt nhất chưa thử theo policy hiện tại.
4. Đọc/validate/prepare token. Nếu lỗi riêng của ứng viên xảy ra trước mutation, ghi nhận nguyên nhân và thử ứng viên kế tiếp.
5. Acquire SessionLock và recheck sessionToken, secureToken, oauthToken từ refresh event.
6. Apply qua API transaction ở bước A.
7. Nếu thành công: cập nhật current, active, selectedEmail, resolvingToken, selection; ghi history một lần; toast success; kết thúc.
8. Nếu thất bại nhưng retry-safe: loại ứng viên khỏi lượt này, recheck baseline trước lần apply tiếp theo và tiếp tục.
9. Nếu session thay đổi, context bị cancel, settings không hợp lệ hoặc rollback không an toàn: dừng lượt, không tiếp tục fallback.
10. Nếu hết ứng viên: giữ nguyên phiên ban đầu. Phân biệt không có quota đủ điều kiện với có ứng viên nhưng mọi lần apply đều thất bại.

### B3. Lock và UI

- Giữ lock cho transaction credential; không chạy quota/OAuth HTTP trong SessionLock.
- Không giữ SessionLock giữa các thao tác không cần thiết. Nếu release/reacquire giữa ứng viên, bắt buộc recheck baseline mỗi lần.
- Tránh thêm thứ tự lock SessionLock -> SettingsLock/AccountsLock chưa được kiểm tra; LoadSettings còn có recoverRestore nên cần xem toàn bộ lock order trước khi gọi bên trong transaction.
- Có thể tải settings ngay trước acquire SessionLock; không tuyên bố settings và session có tính atomic chung vì chúng dùng khóa khác nhau.
- Fallback không được gây vòng retry vô hạn hoặc gọi thêm refresh quota trong cùng lượt.
- Thông báo cuối đủ ngắn cho TUI, chỉ chứa email và lỗi an toàn đã được lọc. Không spam toast cho từng ứng viên thất bại.

## 6. Bước C — Đồng bộ settings CLI vào TUI

### C1. Điểm đồng bộ

- Tái sử dụng ticker kiểm tra credential hiện có, khoảng 1.5 giây, để đọc settings đã normalize.
- Reload một lần nữa ngay trước quyết định auto-next sau quota refresh để nhận thao tác tắt trong lúc refresh đang chạy.
- Chỉ event loop cập nhật state.settings, settingsLoaded, badge và timer. Nếu dùng worker để đọc nhằm tránh chặn UI, worker chỉ gửi snapshot/error qua event; bỏ event settings cũ bằng revision/thứ tự request.
- Không gọi SaveSettings chỉ vì phát hiện thay đổi từ CLI; thao tác này là đọc và đồng bộ bộ nhớ.

### C2. Phát hiện thay đổi và timer

- So sánh revision và trạng thái settingsLoaded; xử lý cả trường hợp config.json bị xóa và trở về default.
- Track interval đang thực sự dùng bởi quotaTicker; so sánh với interval mới trước khi Reset.
- Nếu interval đổi: cập nhật QuotaService.SetCacheTTL và quotaTicker.Reset đúng một lần cho thay đổi đó.
- Không Reset quotaTicker ở mỗi tick 1.5 giây khi interval không đổi, vì có thể khiến refresh bị trì hoãn mãi.
- Đồng bộ các đường thay đổi trong TUI: settings form, settings-reset, toggle-auto-next và việc mở Settings/Profiles view. Chúng không được khiến timer lệch với state.settings.
- Đổi chỉ AutoNext on/off không cần reset lịch quota nếu interval giữ nguyên.
- Không tự kích hoạt switch ngay khi bật từ CLI. Đánh giá ở lượt quota refresh tiếp theo hoặc khi user refresh thủ công, theo semantics hiện có.
- Không ghi đè form settings đang nhập; vẫn bảo toàn kiểm tra revision khi lưu và tránh lost update với CLI.

### C3. Settings lỗi

- Khi config không đọc/parse/normalize được: giữ snapshot hợp lệ cuối để UI còn render được, nhưng đánh dấu settings không đáng tin để chặn auto-switch.
- Báo lỗi ngắn, không phát lại toast giống nhau mỗi 1.5 giây.
- Khi settings hợp lệ trở lại: phục hồi settingsLoaded, badge và timer, rồi tiếp tục quy tắc auto-next bình thường.
- CLI tắt auto-next trong một lượt refresh đang chạy phải ngăn switch khi event refresh được xử lý, nếu việc tắt đã hoàn tất trước lần reload quyết định.
- Thay đổi xảy ra sau lần reload quyết định vẫn là một race giữa hai process; task không thêm transaction chung settings/session. Ghi rõ giới hạn này thay vì hứa atomic tuyệt đối.

## 7. Bước D — README và comment

- Ghi background quota refresh mặc định 300 giây, cấu hình bởi ui.auto_next_interval_seconds; manual refresh vẫn có thể đánh giá auto-next ngay.
- Ghi tuổi tối đa snapshot auto-next 15 phút, lấy theo AutoNextMaxQuotaAge.
- Sửa comment 2 phút trong auto_next.go thành 15 phút hoặc tham chiếu constant để tránh lệch về sau.
- Không thay 2 phút ở readiness của recommend/next: đó là quy tắc riêng, chưa được user yêu cầu sửa.
- Giải thích strict OR: đúng 20%/15% chưa kích hoạt; chỉ một cửa sổ xuống dưới ngưỡng là đủ; giá trị thực có thể khác phần trăm làm tròn.
- Mô tả fallback qua các ứng viên và đồng bộ settings của TUI, chỉ sau khi implementation đúng hành vi đó.
- Giữ mô tả auto-next chỉ hoạt động trong interactive TUI.

## 8. Ma trận kiểm chứng cho agent triển khai

Không tạo file *_test.go mới. Có thể bổ sung/bảo trì case trong file test hiện có theo AGENTS.md; các kịch bản UI/integration kiểm tra bằng môi trường cô lập và credential backend giả, tránh thay phiên thật ngoài ý muốn.

| Kịch bản | Kết quả cần đạt |
| --- | --- |
| Active khỏe, quota cao | Auto-next không switch |
| 5h = 20%, weekly = 15% | Không kích hoạt |
| 5h <20% hoặc weekly <15% | Kích hoạt khi đủ điều kiện khác |
| A thấp quota, B quota tốt nhưng Windows payload >2560 byte, C tốt | B bị loại trong lượt; chuyển C; history chỉ ghi C |
| B lỗi đọc vault sau khi quota đã refresh; C dùng được | Thử C, không bị B chặn |
| B lỗi apply, rollback đầy đủ; C dùng được | Thử C và chỉ ghi một switch thành công |
| B lỗi apply và rollback secure store hoặc một OAuth file lỗi | Dừng, không thử C; báo rollback lỗi |
| Mọi ứng viên thất bại nhưng rollback đầy đủ | Giữ nguyên toàn bộ phiên ban đầu; báo hết ứng viên dùng được |
| Session đổi trước apply hoặc giữa hai lần thử | Dừng, không ghi đè phiên mới |
| Refresh revision cũ | Không apply và không ghi history |
| Active quota refresh lỗi, hoặc store/storage lỗi | Không auto-next |
| Chưa mở TUI / demo mode / overlay / job chưa xong | Giữ semantics bỏ qua hiện tại |
| CLI bật/tắt, TUI đang mở | Badge nhận thay đổi ở nhịp sync; lượt refresh dùng giá trị mới |
| CLI tắt trong lúc quota refresh chạy | Event refresh không switch nếu reload thấy OFF |
| CLI đổi interval 300 ->10 hoặc 10 ->300 | Cache TTL và timer đổi; không tạo nhiều refresh song song |
| Settings không đổi qua nhiều tick | Timer không bị reset liên tục; background refresh vẫn xảy ra |
| Config invalid rồi sửa hợp lệ hoặc xóa config | Chặn switch khi lỗi; phục hồi/default đúng; không spam toast |
| TUI form đang mở đồng thời CLI sửa settings | Không mất input, không ghi đè thay đổi CLI âm thầm |
| Manual switch Windows lỗi payload quá lớn | Báo nguyên nhân hữu ích; phiên trước giữ nguyên |
| next với balanced và active có quota cao nhất | Giữ hành vi hiện có theo phạm vi đã chốt |
| Snapshot auto-next đúng 15 phút / quá 15 phút / tương lai | Đúng biên hiện tại: chấp nhận đúng 15 phút; từ chối quá tuổi hoặc tương lai |

Các bước kiểm tra dự kiến của agent triển khai:

1. Kiểm tra diff: không đổi version, policy, ngưỡng, EOL ngoài phạm vi và không có file test mới.
2. Chạy gofmt trên file Go có chỉnh sửa, rồi giữ lại EOL hiện có theo hướng dẫn project.
3. Chạy go test ./... theo AGENTS.md; chỉ chạy thêm targeted/race check nếu thay đổi concurrency hoặc lỗi thực tế cần kiểm chứng.
4. Kiểm chứng ma trận transaction/fallback/settings bằng các fixture hoặc test hiện có; go test pass một mình chưa chứng minh đủ các kịch bản auto-next.
5. Nếu có cài bản dùng thử, dùng make install BUILD_ID=release theo project; không bump version. Việc cài đặt thuộc agent triển khai, không nằm trong lượt viết plan.
6. Bàn giao diff unstaged, báo những case đã kiểm chứng và giới hạn integration với agy thực tế.

## 9. Thứ tự thực hiện và tiêu chí hoàn thành

1. Transaction credential có lỗi cụ thể và contract retry-safe.
2. Fallback auto-next dùng contract đó; kiểm chứng rollback trước khi retry.
3. Đồng bộ settings/timer và chặn switch khi settings không hợp lệ.
4. README/comment đúng constant và hành vi cuối cùng.
5. Kiểm tra bắt buộc và bàn giao diff.

Hoàn thành khi một ứng viên lỗi không chặn ứng viên dùng được, không có retry sau rollback chưa an toàn, settings từ CLI được TUI nhận đúng, timer không bị starvation, và README ghi đúng 300 giây / 15 phút. Hành vi giữ active khi khỏe và giới hạn chỉ chạy trong TUI được giữ nguyên.

## 10. Ghi chú về lượt review/plan

- Lượt review trước đã chạy go test ./... và pass trên code hiện tại; kết quả đó không phải validation cho implementation tương lai.
- Lượt viết plan này chỉ tạo tài liệu, không implement và không chạy thêm test.
- Hai file untracked agy-swap.exe.3.1.5 và agy-swap.exe.3.1.6 đã có trước task, không nằm trong phạm vi.
