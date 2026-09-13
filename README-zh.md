# git-remote-gdrive

[English](README.md) | [繁體中文](README-zh.md)

[![Testing](https://github.com/jr-dragon/git-remote-gdrive/actions/workflows/testing.yml/badge.svg)](https://github.com/jr-dragon/git-remote-gdrive/actions/workflows/testing.yml)

透過 `gdrive://{folder_id}` 將 Google Drive 作為 Git 遠端儲存庫。本專案提供瀏覽器 OAuth 認證，以及支援 clone、fetch 與 push 的遠端輔助程式（remote helper）。
也可以使用 `gdrive-local:///absolute/path`，在無需 OAuth 或 Drive API 的情況下將本地資料夾作為 remote，再自行傳輸或使用 Google Drive 電腦版同步。

## 安裝

使用 `go.mod` 指定的 Go 版本執行：

```sh
go install ./git-gdrive ./git-remote-gdrive ./git-remote-gdrive-local
```

將 Go 執行檔的安裝目錄加入 `PATH`。也可以執行 `make build`，並將產生的 `build/` 目錄加入 `PATH`。需要 Git 2.36 或更新版本：`git-gdrive` 提供 `git gdrive` 指令，Git 自動呼叫 `git-remote-gdrive` 處理 `gdrive://`，或 `git-remote-gdrive-local` 處理 `gdrive-local://`。本地模式不需要 credential。

## 本地資料夾 remote（不使用 Drive API）

在 Git 工作目錄之外建立專用資料夾，並以絕對路徑設定 remote：

```sh
mkdir -p /absolute/path/Drive/project-remote
git remote add origin 'gdrive-local:///absolute/path/Drive/project-remote'
git push -u origin main
git push origin --tags
```

Windows 使用 `gdrive-local:///C:/Users/me/Drive/project-remote`。包含空格的 URL 請加引號；路徑中的 `%`、`#`、`?` 請分別編碼為 `%25`、`%23`、`%3F`。不支援相對路徑與 UNC 路徑。既有 remote 可用 `git remote set-url origin <local-url>` 修改。

Push 完成後，上傳或複製**整個 root**，包含 `@.git-remote-gdrive/`。其他使用者完整下載資料夾後執行：

```sh
git clone 'gdrive-local:///absolute/path/downloaded/project-remote' project
cd project
# 開發並 commit 後：
git push origin main
```

資料夾包含可搬移的 packs、refs／HEAD／tags、manifests 與選用的 assets；ZIP 會在 refs 發布成功後寫入 `branches/` 與 `tags/`。Assets 沿用 `git gdrive install` 與 `.gitattributes` 設定。若要在 clone 時還原 assets，可事先執行 `git gdrive install --global`；或先 `clone --no-checkout`，在專案內安裝 filter 後再 checkout。

跨電腦協作時，必須協調**同一時間只有一位寫入者**：fetch/pull 前先完成下載，push 後先完成上傳，再交給下一位使用者。使用 Google Drive 電腦版時，請將資料夾設為可離線使用。本地鎖與版本檢查能保護共用同一本地檔案系統的程序，無法鎖定各台電腦獨立同步的副本。若 `CURRENT` 發生同步衝突，請保留雙方副本、透過 Git 整合歷史後，再發布完整資料夾。缺少物件或指標時會回報錯誤，不會自動選擇較舊 manifest。

本地可攜格式與 API 後端的 Drive ID／自訂屬性格式不同：上傳本地 remote **不會**使其支援 `gdrive://`，下載 API remote 也不會自動轉換。遷移時，請從已取得所需歷史與 assets 的 Git checkout，新增空白目的地 remote，再 push 所需 branches 與 tags。詳見[本地儲存格式](docs/storage-local.md)。

## Google OAuth 設定

1. 建立 Google Cloud 專案並啟用 Google Drive API。
2. 設定 OAuth 同意畫面。如果應用程式處於測試階段，請將你的 Google 帳戶加入測試使用者。
3. 建立應用程式類型為 **Desktop app（桌面應用程式）** 的 OAuth 用戶端，並下載其用戶端 JSON 檔案。請將此檔案保存在儲存庫之外。
4. 執行：

   ```sh
   git-gdrive config --client-file /path/to/client_secret.json
   ```

也可以將 `GIT_GDRIVE_CLIENT_FILE` 設為該檔案路徑，再執行 `git gdrive config`。指令會開啟預設瀏覽器、要求離線存取權限，並等待授權，最長五分鐘。按 Ctrl+C 可取消。

要求的權限範圍為 `https://www.googleapis.com/auth/drive`，允許讀取及管理所有 Drive 檔案，以支援直接透過 ID 存取既有資料夾的使用方式。權限較小的 `drive.file` 範圍要求檔案由應用程式建立，或明確授權應用程式存取，例如透過 Google Picker；單純輸入資料夾 ID 並不會授予該存取權限。請參閱 [Google Drive 權限範圍文件](https://developers.google.com/workspace/drive/api/guides/api-specific-auth)。

成功後，指令會以原子方式將 JSON 憑證寫入：

```text
~/.config/git-remote-drive/credential
```

檔案包含 OAuth 用戶端 ID 與密鑰、存取權杖、更新權杖、權杖類型及到期時間。在 Unix 上，目錄權限為 `0700`，檔案權限為 `0600`。授權失敗時，既有憑證會保持不變。憑證包含敏感資訊，且以明文儲存；請勿將其納入版本控制。

## 手動 callback 備援方式

一般情況下，程式會啟動綁定至 `127.0.0.1` 的臨時 HTTP 接聽器，接收 Google 的重新導向。如果無法啟動接聽器，指令會自動切換為手動輸入。也可以強制使用此模式：

```sh
git-gdrive config --client-file /path/to/client_secret.json --manual
```

如果瀏覽器沒有開啟，請自行開啟程式顯示的授權 URL。授予權限後，瀏覽器可能會顯示無法連線至迴路位址的錯誤（手動模式使用 `http://127.0.0.1:1/oauth2/callback`）。請複製**網址列中最後顯示的完整 URL**，貼到終端機並按 Enter。瀏覽器與 CLI 位於不同電腦時，也可以使用此方式。URL 必須屬於本次授權流程；不接受單獨的授權碼或存取權杖。自動與手動流程都會驗證 state 並使用 PKCE。

這裡使用桌面應用程式的迴路重新導向，並非 Google 已停用的 OOB 流程。請參閱 [Google 原生應用程式 OAuth 文件](https://developers.google.com/identity/protocols/oauth2/native-app)。

## 專案結構與檢查

- `git-gdrive/main.go`：指令解析與設定流程。
- `git-remote-gdrive/main.go`：遠端輔助程式進入點。
- `internal/remotehelper`：Git 協定、ref 探索與 push 驗證。
- `internal/repository`：版本化 manifest、pack 產生與物件驗證。
- `internal/assets`：asset pointer 與經驗證的內容快取。
- `internal/gdriveassets`：可選的 filter 安裝、clean/smudge 指令與 asset 取得流程。
- `internal/drive`：Drive 儲存、條件式發布、重試與可續傳上傳。
- `internal/googleauth`：OAuth 流程、callback 驗證與憑證儲存。
- `internal/browser`：macOS、Linux 與 Windows 的預設瀏覽器啟動功能。

```sh
go test -race ./...
go vet ./...
```

測試使用本機模擬的 OAuth／Drive 端點與臨時儲存庫。整合測試會對模擬 Drive 伺服器執行真正的 Git 指令，涵蓋 push、clone、fetch、並行初始化、資料損毀、上傳中斷與 pack 整併。測試不會連線至 Google，也不會使用真實憑證。

## 使用 Drive 儲存庫

建立 Google Drive 資料夾，並從瀏覽器 URL 複製資料夾 ID。先完成 `git gdrive config`，再推送既有儲存庫：

```sh
git remote add origin gdrive://YOUR_FOLDER_ID
git push -u origin HEAD
git push origin --tags
```

第一次成功的 push 會初始化資料夾內的儲存結構。其他有權存取同一資料夾的使用者，可以使用自己的 Google 帳戶完成認證後執行：

```sh
git clone gdrive://YOUR_FOLDER_ID
git fetch origin
git push origin HEAD
```

讀取者需要有權存取資料夾及其內容。寫入者還需要建立檔案及更新根資料夾中繼資料的權限。共用設定由 Google Drive 管理；輔助程式不會更改權限。每位使用者都可以使用自己的桌面 OAuth 用戶端，因為儲存庫探索使用 Drive 的公開屬性（對已授權的應用程式可見，並不代表檔案內容公開可供存取）。

輔助程式支援分支、輕量標籤與附註標籤、ref 刪除、明確指定的強制推送、試執行（dry run），以及原子性的批次 push。除非要求強制推送，否則會拒絕已分歧的分支更新與既有標籤的替換。第一次 push 會盡可能將推送的本機預設分支設為遠端 HEAD；否則會選擇排序後的第一個分支。HEAD 會維持不變，直到該分支被刪除；若仍有其他分支，則改為指向其中一個。

## 傳輸進度

Git 啟用進度顯示時，clone、push、pull 與 fetch 會將所有檔案合併為一個遠端輔助程式任務，並在 stderr 顯示任務整體百分比、目前的 `file N/總數`、目前檔案百分比與位元組數。Push 會在開始上傳前盤點 pack、asset、ZIP 與 manifest；fetch 則會將所有缺少的 pack 下載合併計算。如果 push 必須先取得驗證所需的舊 packs，這些檔案會先顯示在同一任務中，接著才確定最終總數。除了開始與結束訊息外，每個檔案最多每秒更新一次。重試使用絕對位移計算進度；失敗檔案不會增加整體完成數，只有所有預定檔案都成功後才會顯示 100%。Refs 發布會在條件式更新成功後另行顯示。

Git 通常會在互動式終端機中啟用進度。若要在輸出被重新導向時強制顯示進度，請使用：

```sh
git clone --progress gdrive://YOUR_FOLDER_ID
git push --progress origin HEAD
git pull --progress
```

`--quiet` 與 `--no-progress` 會關閉遠端輔助程式的進度訊息；錯誤與 ZIP 匯出警告仍會顯示。遠端輔助程式階段結束後，checkout／merge 的輸出由 Git 自行控制。

## 可選的大型資產管理

使用 `gdrive-assets`，可以透過小型 Git 指標檔（pointer）對執行檔或函式庫進行版本控制，並將實際內容儲存在 Drive。這是可選功能；一般儲存庫不需要設定 filter。它採用 Git 的 [clean/smudge filter 機制](https://git-scm.com/docs/gitattributes)，使用自己的 pointer 格式與 Drive 儲存方式，不需要安裝 Git LFS。

在儲存庫內安裝 filter：

```sh
git gdrive install
```

將檔案比對規則加入 `.gitattributes`，並將此檔案與 assets 一起提交：

```gitattributes
*.so  filter=gdrive-assets diff=gdrive-assets merge=gdrive-assets -text
*.dll filter=gdrive-assets diff=gdrive-assets merge=gdrive-assets -text
dist/** filter=gdrive-assets diff=gdrive-assets merge=gdrive-assets -text
```

```sh
git add .gitattributes dist/
git commit -m "Track release assets"
git push origin HEAD
```

`.gitignore` 仍然適用：如果要對被忽略的 asset 進行版本控制，請明確使用 `git add -f`。對於設定 filter 前就已被追蹤的檔案，請使用 `git add --renormalize -- path/to/asset` 並提交轉換結果。既有歷史會保留，不會被改寫。

`git add` 會在本機快取內容，並將包含 SHA-256 與大小的 pointer 加入暫存區。Push 會先上傳遠端尚未儲存的 assets，包括歷史 commits 所需的版本，再發布 refs。同一儲存庫內，位元組內容相同的檔案會共用一個 Drive 物件。若 asset 內容缺失或上傳失敗，push 會失敗，且不會更新 refs。原有的 ZIP 匯出仍會在 refs 提交成功後執行；ZIP 中的對應項目會包含已提交的 pointers。

每位協作者都必須在自己的電腦上安裝 filter。若要在 clone 前，為各儲存庫中符合規則的路徑啟用 filter：

```sh
git gdrive install --global
git clone gdrive://YOUR_FOLDER_ID
```

也可以只在新 clone 的儲存庫中安裝：

```sh
git clone --no-checkout gdrive://YOUR_FOLDER_ID repo
cd repo
git gdrive install
git checkout HEAD -- .
```

Checkout 會按需下載 assets，並驗證大小與 SHA-256。已快取的內容可離線使用。如果未安裝 filter，checkout 會得到 pointers。若已安裝 filter，但想明確保留 pointers，請設定 `GIT_GDRIVE_SKIP_SMUDGE=1`。

第一次推送 asset 會將遠端 manifest 升級為 v2；該儲存庫的所有協作者都需要更新輔助程式。從未使用 assets 的儲存庫會維持 v1。Asset 物件與快取內容都會保留；目前尚未實作自動垃圾回收。請參閱 [asset 格式與行為](docs/assets.md)。

## 儲存格式與限制

每個推送的分支或標籤，也會在所選 Drive 根資料夾下產生 ZIP 快照：

```text
YOUR_FOLDER_ID/
  branches/
    main.zip
    feature%2Flogin.zip
  tags/
    v1.0.zip
  @.git-remote-gdrive/
    ...packs and manifests...
```

壓縮檔名稱使用目的分支／標籤名稱，不包含物件 ID。Ref 名稱會經過百分比編碼，因此 `feature/login` 會變成 `feature%2Flogin`，並維持為單一檔名。附註標籤的 ZIP 會包含解參照後版本的檔案。ZIP 遵循 `git archive` 的行為，包括 `export-ignore`／`export-subst` 屬性。內容包含已追蹤的檔案，不包含 `.git`、尚未提交的變更或未追蹤的檔案；也不會取得 submodule 的內容。直接指向 blob 的標籤會產生只有一個名為 `blob` 的檔案的 ZIP。

輔助程式會在需要時才建立 `branches/` 與 `tags/`，並將其 Drive ID 記錄在 manifest 中，供不同使用者重複使用。Push 會先上傳 pack 並發布 refs；成功後才產生、上傳或覆寫 ZIP。成功的匯出結果會透過另一次條件式 manifest 更新來記錄，包含各 ZIP 的 ID、物件 ID、大小與 SHA-256 摘要。同名 ZIP 會使用既有的 Drive 檔案 ID 原地覆寫；只有不存在時才會建立新的 ZIP。刪除 ref 會移除目前的對應記錄，但保留最後一份 ZIP；重新建立 ref 時會重複使用該檔案。試執行與未變更的 refs 不會上傳 ZIP。舊儲存庫仍可讀取；其分支／標籤更新時會加入 ZIP。

既有且由 manifest 指定的 `branch/` 資料夾，會在下一次分支 ZIP 匯出時改名為 `branches/`，保留資料夾與 ZIP 檔案的 ID。Manifest 會保留 `branch` 鍵以維持相容性。寫入這些資料夾時，請使用更新後的輔助程式。

如果 ref 發布失敗，ZIP 不會被變更。如果後續的 ZIP 產生、上傳或中繼資料更新失敗，Git push 仍維持成功，並在標準錯誤輸出（stderr）顯示警告，不會回滾 refs。發布 refs 時，已變更 ref 的舊 ZIP 對應記錄會被移除，因此缺少對應記錄表示匯出尚未確認或不存在。舊 ZIP 檔案可能會保留到下一次成功更新。已是最新狀態的 push 不會重試失敗的匯出；之後更新 ref 時，才會再次嘗試匯出。

Drive 不支援跨檔案交易：ZIP 可能暫時落後於 refs，或缺少已確認的中繼資料。輔助程式會在匯出前重新載入目前的 refs，略過再次變更的 refs，在覆寫前檢查 manifest 版本，並在開始更新 ZIP 時傳送其 ETag。之後的並行 push 仍可能與進行中的上傳發生競爭。ZIP 校驗碼描述已確認的匯出內容；只有 packs 與 manifests 保存作為依據的 Git 歷史。既有的舊式 `<name>-<object-id>.zip` 檔案會保持不變；新的 push 使用固定名稱。輔助程式不會採用無關的同名資料夾；若壓縮檔目錄中有多個符合條件的 ZIP，會因無法唯一辨識而回報錯誤。請使用更新後的輔助程式進行 push，以保留 ZIP 追蹤記錄。

根資料夾的公開 `gdrive-repo` 屬性用來辨識正式使用的 `@.git-remote-gdrive/` 目錄。`gdrive-manifest` 屬性則透過 Drive 檔案 ID，指向目前不可變的 `manifest.json`。Drive 允許重複檔名，因此用戶端會依據這些 ID 存取，而不會按名稱選擇資料夾或 manifest。

目錄中包含不可變的 pack 檔案與 manifest 快照。Manifest 記錄格式版本、SHA-1 物件格式、儲存庫／目錄 ID、符號參照 HEAD、每個 ref 的物件 ID、附註標籤解參照後的物件 ID，以及依序排列的 pack ID、大小、Git pack 雜湊與 SHA-256 摘要。所有使用者都透過這份共用記錄取得相同的分支與標籤。

每次 push 最多上傳一個非 thin 的增量 pack，包含無法從先前 refs 到達的物件。當目前使用的 pack 鏈已有 16 個 packs，下一次 push 會寫入完整 pack 並取代該 pack 鏈。Clone／fetch 會下載該快照所需的 packs，並重複使用 Git 本機物件儲存區中已有的 packs。下載內容會經過驗證，再使用 `git index-pack --strict` 匯入；回報成功前，也會檢查物件連通性。物件不會以個別鬆散檔案的形式上傳。

上傳使用可續傳工作階段，以 8 MiB 為區塊傳送，並在請求中斷後查詢伺服器已確認的位移。對於 HTTP 429、可重試的 HTTP 403 速率限制回應、特定 HTTP 5xx 錯誤與連線錯誤，請求會使用有次數上限的指數退避、隨機延遲與 `Retry-After` 進行重試。權限與儲存配額錯誤會立即失敗。預先產生的檔案 ID 可讓重試建立檔案時保持冪等性。

Pack 與 manifest 上傳完成後才會發布。輔助程式會重新讀取根資料夾的指標，並在中繼資料 PATCH 請求的 `If-Match` 中使用其 Drive v2 ETag。過期的快照或 HTTP 412 會使 push 被拒絕，即使是強制推送也一樣；請先 fetch 再重試。此 PATCH 請求會一併發布目錄與 manifest ID。如果缺少 ETag，程式會拒絕更新。用戶端使用 Drive v2，因為其檔案中繼資料提供 ETag。

目前的限制與成本：

- 格式 v1 支援 SHA-1 與完整歷史。不接受使用 SHA-256 物件格式或淺層歷史的本機儲存庫；也不宣告支援淺層／部分傳輸選項。
- Fetch 會傳輸目前快照使用的 packs，包括所要求分支以外的 refs 所含的物件。Pack 整併會定期進行完整上傳，以限制目前使用的 pack 數量。
- 舊式 ZIP、manifests、已被取代的 packs，以及中斷或衝突的 push 遺留的檔案，都會保留。這可保護仍在使用舊快照的讀取者，但會消耗 Drive 配額。目前尚未實作自動垃圾回收；讀取或寫入作業進行中時，請勿刪除檔案。
- 每個更新的分支／標籤，除了 Git pack 資料之外，都會額外上傳完整 ZIP 快照。這會增加與快照大小成正比的傳輸時間與 Drive 儲存空間。
- 除了 Git 物件儲存區外，建立 ZIP、建立／下載 pack 都需要本機暫存磁碟空間。傳輸所用記憶體的上限取決於上傳區塊大小，而非整個 pack 的大小。Manifest 輸入上限為 8 MiB。
- Google Drive 的實際行為，包括 ETag 發布與共用雲端硬碟存取，仍需要使用真實憑證進行整合測試。自動化測試驗證的是模擬 API 下的協定行為，而非 Google 已部署的服務。

詳細格式與提交演算法請參閱 [docs/storage.md](docs/storage.md)。

## GitHub Actions

每次 push 至 `main` 時，**Testing** 工作流程都會執行 `go test -race ./...` 與 `go vet ./...`。兩個工作流程都使用 `go.mod` 宣告的 Go 版本。

發布 GitHub Release（包含預先發行版本）會針對該標籤觸發 **Build**。它會停用 CGO，為以下目標平台交叉編譯三個執行檔：

| 平台 | 發佈壓縮檔 |
| --- | --- |
| Linux x86_64 | `git-remote-gdrive-linux-x86_64.tar.gz` |
| Linux ARM64 | `git-remote-gdrive-linux-arm64.tar.gz` |
| macOS ARM64（Apple Silicon） | `git-remote-gdrive-macos-arm64.tar.gz` |
| Windows x86_64 | `git-remote-gdrive-windows-x86_64.zip` |
| Windows ARM64 | `git-remote-gdrive-windows-arm64.zip` |

每個壓縮檔包含 `git-gdrive`、`git-remote-gdrive`、`git-remote-gdrive-local`（Windows 版本附有 `.exe` 副檔名）、LICENSE 與 README。工作流程會使用內建的 `GITHUB_TOKEN`，將壓縮檔及其 `.sha256` 校驗碼附加至觸發建置的 release，不需要額外的 secret。重新執行建置會取代該平台對應的 assets。Release 必須允許上傳／替換 assets；此工作流程不會啟用不可變 release。ARM 目標指的是 64 位元 ARM，而非 32 位元 ARMv7。
