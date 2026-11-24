use crate::engine::{Engine, Failure};
use serde::Serialize;
use std::{
    sync::Arc,
    time::{Duration, Instant},
};
#[cfg(target_os = "linux")]
use tauri::Manager;
use tauri::{ipc::Channel, utils::config::BundleType, AppHandle, State};
use tauri_plugin_opener::OpenerExt;
use tauri_plugin_updater::{Error as UpdaterError, Update, UpdaterExt};
use tokio::{sync::Mutex, time::timeout};

const RELEASES: &str = "https://github.com/TNT-Likely/telegram-nestfetch/releases";
const RELEASE_DOWNLOAD_PATH: &str = "/TNT-Likely/telegram-nestfetch/releases/download/";

#[derive(Default)]
pub struct Updates(Mutex<Option<PendingUpdate>>);

struct PendingUpdate {
    update: Update,
    bytes: Option<Arc<Vec<u8>>>,
}

#[derive(Serialize)]
pub struct UpdateInfo {
    version: String,
    installable: bool,
}

#[derive(Clone, Serialize)]
#[serde(
    tag = "event",
    rename_all = "camelCase",
    rename_all_fields = "camelCase"
)]
pub enum UpdateProgress {
    Started {
        total_bytes: Option<u64>,
    },
    Progress {
        downloaded_bytes: u64,
        total_bytes: Option<u64>,
    },
    Installing,
}

fn can_install(_app: &AppHandle) -> bool {
    match tauri::utils::platform::bundle_type() {
        Some(BundleType::App | BundleType::Dmg) => std::env::current_exe().is_ok_and(|path| {
            path.parent().is_some_and(|directory| {
                directory.ends_with("Contents/MacOS")
                    && directory
                        .ancestors()
                        .nth(2)
                        .and_then(|bundle| bundle.extension())
                        == Some("app".as_ref())
            })
        }),
        #[cfg(target_os = "linux")]
        Some(BundleType::AppImage) => _app
            .env()
            .appimage
            .is_some_and(|image| std::path::Path::new(&image).is_file()),
        Some(BundleType::Nsis) => true,
        _ => false,
    }
}

fn official_release_asset(update: &Update) -> bool {
    let url = &update.download_url;
    let release = format!("{RELEASE_DOWNLOAD_PATH}v{}/", update.version);
    url.scheme() == "https"
        && url.host_str() == Some("github.com")
        && url.port().is_none()
        && url.username().is_empty()
        && url.password().is_none()
        && url.query().is_none()
        && url.fragment().is_none()
        && url
            .path()
            .strip_prefix(&release)
            .is_some_and(|name| !name.is_empty() && !name.contains('/'))
}

fn download_failure(error: UpdaterError) -> Failure {
    let code = match error {
        UpdaterError::Minisign(_)
        | UpdaterError::Base64(_)
        | UpdaterError::SignatureUtf8(_)
        | UpdaterError::MissingSignedVersion
        | UpdaterError::SignedVersionMismatch { .. } => "update_invalid",
        _ => "update_download_failed",
    };
    Failure::new(code)
}

#[tauri::command]
pub async fn check_updates(
    app: AppHandle,
    updates: State<'_, Updates>,
) -> Result<Option<UpdateInfo>, Failure> {
    let mut pending = updates
        .0
        .try_lock()
        .map_err(|_| Failure::new("update_busy"))?;
    let updater = app
        .updater_builder()
        .configure_client(|client| {
            client
                .connect_timeout(Duration::from_secs(10))
                .read_timeout(Duration::from_secs(45))
        })
        .build()
        .map_err(|_| Failure::new("update_check_failed"))?;
    let update = timeout(Duration::from_secs(20), updater.check())
        .await
        .map_err(|_| Failure::new("update_check_failed"))?
        .map_err(|_| Failure::new("update_check_failed"))?;
    let Some(update) = update else {
        *pending = None;
        return Ok(None);
    };
    if !official_release_asset(&update) {
        *pending = None;
        return Err(Failure::new("update_invalid"));
    }
    let info = UpdateInfo {
        version: update.version.clone(),
        installable: can_install(&app),
    };
    *pending = Some(PendingUpdate {
        update,
        bytes: None,
    });
    Ok(Some(info))
}

#[tauri::command]
pub async fn install_update(
    app: AppHandle,
    engine: State<'_, Engine>,
    updates: State<'_, Updates>,
    on_progress: Channel<UpdateProgress>,
) -> Result<(), Failure> {
    if !can_install(&app) {
        return Err(Failure::new("update_unavailable"));
    }
    let mut guard = updates
        .0
        .try_lock()
        .map_err(|_| Failure::new("update_busy"))?;
    let pending = guard
        .as_mut()
        .ok_or_else(|| Failure::new("update_unavailable"))?;
    if pending.bytes.is_none() {
        let mut downloaded_bytes = 0;
        let mut last_report = Instant::now();
        let bytes = pending
            .update
            .download(
                |chunk, total_bytes| {
                    if downloaded_bytes == 0 {
                        let _ = on_progress.send(UpdateProgress::Started { total_bytes });
                    }
                    downloaded_bytes += chunk as u64;
                    if last_report.elapsed() >= Duration::from_millis(120)
                        || total_bytes == Some(downloaded_bytes)
                    {
                        let _ = on_progress.send(UpdateProgress::Progress {
                            downloaded_bytes,
                            total_bytes,
                        });
                        last_report = Instant::now();
                    }
                },
                || {},
            )
            .await
            .map_err(download_failure)?;
        pending.bytes = Some(Arc::new(bytes));
    }

    // Reserve the idle queue once verification has completed. Keep verified bytes
    // if a transfer started during the update download and installation must wait.
    engine.prepare_update().await?;
    let _ = on_progress.send(UpdateProgress::Installing);
    let update = pending.update.clone();
    let bytes = pending
        .bytes
        .as_ref()
        .expect("the updater verified the package")
        .clone();
    match tauri::async_runtime::spawn_blocking(move || update.install(bytes.as_slice())).await {
        Ok(Ok(())) => {
            *guard = None;
            app.request_restart();
            Ok(())
        }
        _ => {
            engine.finish_update().await;
            Err(Failure::new("update_install_failed"))
        }
    }
}

#[tauri::command]
pub fn open_release_page(app: AppHandle) -> Result<(), Failure> {
    app.opener()
        .open_url(RELEASES, None::<&str>)
        .map_err(|_| Failure::new("open_release_failed"))
}
