#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod engine;

use engine::{Engine, Failure};
use serde_json::{json, Value};
use std::path::PathBuf;
use tauri::{path::BaseDirectory, AppHandle, Manager, RunEvent, State};
use tauri_plugin_dialog::DialogExt;
use tauri_plugin_opener::OpenerExt;

const TELEGRAM_SETTINGS: &str = "https://my.telegram.org/apps";
const PROJECT_REPOSITORY: &str = "https://github.com/TNT-Likely/telegram-nestfetch";

#[tauri::command]
async fn engine_request(
    engine: State<'_, Engine>,
    method: String,
    params: Option<Value>,
) -> Result<Value, Failure> {
    match method.as_str() {
        "snapshot" | "configure" | "begin_login" | "login_password" | "cancel_login"
        | "sign_out" | "set_folder" | "enqueue" | "pause" | "resume" | "retry"
        | "save_elsewhere" | "thumbnail" | "enqueue_batch" | "channels_start" | "channels_status"
        | "attachments_start" | "attachments_status" | "browse_cancel" | "preview_start"
        | "preview_status" | "preview_stop" => engine.request(&method, params).await,
        _ => Err(Failure::new("invalid_request")),
    }
}

#[tauri::command]
async fn choose_folder(app: AppHandle, initial: String) -> Result<Option<String>, Failure> {
    let picked = tauri::async_runtime::spawn_blocking(move || {
        let mut dialog = app.dialog().file();
        let initial = PathBuf::from(initial);
        if initial.is_absolute() && initial.is_dir() {
            dialog = dialog.set_directory(initial);
        }
        dialog.blocking_pick_folder()
    })
    .await
    .map_err(|_| Failure::new("picker_failed"))?;

    picked
        .map(|path| {
            path.into_path()
                .ok()
                .and_then(|path| path.into_os_string().into_string().ok())
                .ok_or_else(|| Failure::new("picker_failed"))
        })
        .transpose()
}

#[tauri::command]
async fn reveal_job(app: AppHandle, engine: State<'_, Engine>, id: String) -> Result<(), Failure> {
    let value = engine
        .request("reveal_path", Some(json!({ "id": id })))
        .await?;
    let path = value
        .as_str()
        .map(PathBuf::from)
        .filter(|path| path.is_absolute())
        .ok_or_else(|| Failure::new("reveal_failed"))?;

    tauri::async_runtime::spawn_blocking(move || {
        let result = app.opener().reveal_item_in_dir(&path);
        #[cfg(target_os = "linux")]
        if result.is_err() && path.is_file() {
            if let Some(parent) = path.parent() {
                return app
                    .opener()
                    .open_path(parent.to_string_lossy(), None::<&str>)
                    .map_err(|_| Failure::new("reveal_failed"));
            }
        }
        result.map_err(|_| Failure::new("reveal_failed"))
    })
    .await
    .map_err(|_| Failure::new("reveal_failed"))?
}

#[tauri::command]
fn open_telegram_settings(app: AppHandle) -> Result<(), Failure> {
    app.opener()
        .open_url(TELEGRAM_SETTINGS, None::<&str>)
        .map_err(|_| Failure::new("open_failed"))
}

#[tauri::command]
fn open_project_repository(app: AppHandle) -> Result<(), Failure> {
    app.opener()
        .open_url(PROJECT_REPOSITORY, None::<&str>)
        .map_err(|_| Failure::new("open_failed"))
}

fn main() {
    let app = tauri::Builder::default()
        .plugin(tauri_plugin_single_instance::init(|app, _, _| {
            if let Some(window) = app.get_webview_window("main") {
                let _ = window.show();
                let _ = window.set_focus();
            }
        }))
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_opener::init())
        .setup(|app| {
            let paths = app.path();
            let data = paths.app_data_dir()?;
            let downloads = match paths.download_dir() {
                Ok(path) => path,
                Err(_) => {
                    let fallback = data.join("Downloads");
                    std::fs::create_dir_all(&fallback)?;
                    fallback
                }
            };
            let binary = std::env::current_exe()?.with_file_name(if cfg!(windows) {
                "nestfetch-engine.exe"
            } else {
                "nestfetch-engine"
            });
            let helper = paths
                .resolve("tg-nestfetch-frame", BaseDirectory::Resource)
                .ok();
            app.manage(Engine::new(binary, data, downloads, helper));
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            engine_request,
            choose_folder,
            reveal_job,
            open_telegram_settings,
            open_project_repository
        ])
        .build(tauri::generate_context!())
        .expect("failed to start TG NestFetch");

    app.run(|handle, event| {
        if let RunEvent::Exit = event {
            tauri::async_runtime::block_on(handle.state::<Engine>().shutdown());
        }
    });
}
