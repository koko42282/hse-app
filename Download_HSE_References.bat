@echo off
chcp 65001 >nul
title Usb Ai - Download HSE references
echo.
echo  ==============================================
echo    Usb Ai  -  Download HSE references (needs Internet)
echo  ==============================================
echo.
echo  Files will be saved in:  %~dp0rag\
echo  (edit tools\refs_list.txt to add your own links)
echo.
if not exist "%~dp0tools\download_refs.ps1" (
  echo  ERROR: tools\download_refs.ps1 is missing.
  pause & exit /b 1
)
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0tools\download_refs.ps1" -Root "%~dp0."
echo.
pause
