; electron-builder 26.18.0 falls back to English for these three messages.
!macro customHeader
  !pragma warning disable 6030 ; The generated language file has already defined these labels.
  LangString decompressionFailed ${LANG_SIMPCHINESE} "无法解压安装文件。"
  LangString uninstallFailed ${LANG_SIMPCHINESE} "无法卸载旧版本，安装已停止。"
  LangString appClosing ${LANG_SIMPCHINESE} "正在关闭 ${PRODUCT_NAME}…"
  !pragma warning default 6030
!macroend
