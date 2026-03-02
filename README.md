# CRadio

在終端機裡收聽線上廣播電台。

![image](image.png)

## 系統需求

- Go 1.20+ (如要自行編譯原始碼)
- mpv (必要)

CRadio 使用 mpv 作為子行程播放線上廣播電台，所以需要先安裝 mpv。

### 安裝 mpv

- Windows：在 Microsoft Store 裡安裝 [mpv (Unofficial)](https://apps.microsoft.com/detail/9P3JFR0CLLL6?hl=neutral&gl=TW&ocid=pdpshare)。

- macOS：
    ```
    brew install mpv
    ```
- Ubuntu/Debian：
    ```
    sudo apt install mpv
    ```

## 下載執行檔 (Windows)

從 [GitHub Releases](https://github.com/riddleling/cradio/releases) 頁面下載 `CRadio.zip`，並解壓縮。

## 執行 CRadio

- 執行 `cradio.exe`
- 檔案 `list.json` 是電台列表，你可自行編輯與維護此列表。

## 從原始碼編譯與執行

```
git clone https://github.com/riddleling/cradio.git
cd cradio
go mod tidy
go build
./cradio
```

## 鍵盤操作

- `↑`、`↓`：選台
- `Enter`：播放/切台
- `s`：停止播放
- `q`：退出程式
- `/`：搜尋電台

## 子專案

[RadioListUpdater](https://github.com/riddleling/RadioListUpdater) 是 CRadio 的子專案，用來更新電台列表 (`台北愛樂電台`與`港都電台`)。

## 問題

- macOS 上如果播放沒聲音，請參考 [issue #1](https://github.com/riddleling/cradio/issues/1) 重新安裝 mpv。

## License

MIT License
