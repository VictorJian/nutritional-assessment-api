# nutritional-assessment-api

營養評估表後端 API。使用 Go 實作，負責接收前端送出的評估資料、產生紀錄 ID、儲存資料，並提供 ID 或姓名查詢。

## 功能

- 接收前端送出的 JSON 評估資料。
- 使用建立時間產生 `YYYYMMDDHHMMSS` 格式紀錄 ID。
- 如果同一秒已有相同 ID，會自動往後加秒數避免衝突。
- 可將資料儲存在 PostgreSQL；未設定資料庫時，改用本機 JSON 檔案。
- 支援用 ID 查詢單筆紀錄。
- 支援用姓名查詢多筆紀錄。
- 會在 terminal / Render Logs 記錄 `GET` 和 `POST` request。
- 已開啟 CORS，前端可從不同來源呼叫 API。
- 保留 `/api/send` 舊路徑相容。

## 啟動

```sh
go run .
```

API 預設執行在：

```text
http://localhost:8080
```

## 環境變數

### `PORT`

設定服務 port。預設是 `8080`。

```sh
PORT=8080 go run .
```

### `DATA_FILE`

未設定 `DATABASE_URL` 時，設定本機 JSON 儲存檔案。預設是：

```text
data/assessments.json
```

範例：

```sh
DATA_FILE=data/assessments.json go run .
```

### `DATABASE_URL`

設定 PostgreSQL 連線字串。若有設定，後端會使用 PostgreSQL 儲存資料；若未設定，後端會使用 `DATA_FILE` 的本機 JSON 檔案。

Render 部署時可放入 Neon 提供的 connection string：

```text
DATABASE_URL=postgresql://user:password@xxxx.neon.tech/neondb?sslmode=require&channel_binding=require
```

## API 端點

### `GET /health`

檢查服務狀態。

回應範例：

```json
{
  "ok": true
}
```

### `GET /ping`

確認 API 服務可正常回應。

回應範例：

```json
{
  "message": "pong"
}
```

### `POST /api/assessments`

建立一筆營養評估紀錄。

前端會送出包含 respondent、answers、totals、recommendations 的 JSON。後端會原樣保存到 `data` 欄位，並加上 ID 與建立時間。

回應範例：

```json
{
  "id": "20260708110511",
  "createdAt": "2026-07-08T11:05:11+08:00",
  "data": {
    "respondent": {
      "name": "王小明",
      "age": 30,
      "date": "2026-07-08"
    }
  }
}
```

### `GET /api/assessments/:id`

用紀錄 ID 查詢單筆資料。

範例：

```sh
curl http://localhost:8080/api/assessments/20260708110511
```

### `GET /api/assessments?name=<name>`

用姓名查詢符合的紀錄。

範例：

```sh
curl "http://localhost:8080/api/assessments?name=王小明"
```

回應會包含：

```json
{
  "query": "王小明",
  "count": 1,
  "records": []
}
```

## 相容路徑

為了相容既有前端，也支援：

- `POST /api/send`
- `GET /api/send/:id`

## CORS

後端已開啟 CORS：

- `Access-Control-Allow-Origin: *`
- `Access-Control-Allow-Methods: GET, POST, OPTIONS`
- 支援瀏覽器預檢 `OPTIONS`
- 支援前端 JSON request headers

## 資料儲存

如果有設定 `DATABASE_URL`，資料會寫入 PostgreSQL 的 `assessments` table。建議在 Neon SQL Editor 建立：

```sql
CREATE TABLE IF NOT EXISTS assessments (
  id TEXT PRIMARY KEY,
  created_at TIMESTAMPTZ NOT NULL,
  data JSONB NOT NULL
);

CREATE INDEX IF NOT EXISTS assessments_name_idx
ON assessments ((lower(data #>> '{respondent,name}')));
```

程式啟動時也會自動執行 `CREATE TABLE IF NOT EXISTS` 和 `CREATE INDEX IF NOT EXISTS`，避免新環境缺少 table。

如果沒有設定 `DATABASE_URL`，資料預設儲存在：

```text
data/assessments.json
```

檔案會以紀錄 ID 作為 key。若要改變本機儲存位置，可使用 `DATA_FILE`。

## Request Log

後端會在 terminal 或 Render Logs 記錄 `GET` 和 `POST` request，例如：

```text
GET /ping -> 200 (1ms)
POST /api/assessments name=王小明 date=2026-07-09 answers=12
POST /api/assessments -> 201 (42ms)
GET /api/assessments/20260709110511 -> 200 (8ms)
```

## 測試

執行：

```sh
go test ./...
```

如果本機 Go cache 權限受限，可以指定可寫入的 cache：

```sh
GOCACHE=/private/tmp/nutritional-assessment-api-go-cache go test ./...
```
