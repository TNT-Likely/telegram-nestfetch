use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::{path::PathBuf, process::Stdio, time::Duration};
use tokio::{
    io::{AsyncBufReadExt, AsyncReadExt, AsyncWriteExt, BufReader},
    process::{Child, ChildStdin, ChildStdout, Command},
    sync::Mutex,
    time::timeout,
};

const REQUEST_DEADLINE: Duration = Duration::from_secs(45);
const MAX_REPLY: u64 = 8 * 1024 * 1024;

#[derive(Debug, Deserialize, Serialize)]
pub struct Failure {
    code: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    message: Option<String>,
}

impl Failure {
    pub fn new(code: &str) -> Self {
        Self {
            code: code.into(),
            message: None,
        }
    }
}

pub struct Engine {
    executable: PathBuf,
    data_dir: PathBuf,
    downloads: PathBuf,
    frame_helper: Option<PathBuf>,
    process: Mutex<Option<Process>>,
}

struct Process {
    child: Child,
    input: ChildStdin,
    output: BufReader<ChildStdout>,
    next_id: u64,
}

#[derive(Deserialize)]
struct Reply {
    id: u64,
    result: Option<Value>,
    error: Option<Failure>,
}

impl Engine {
    pub fn new(
        executable: PathBuf,
        data_dir: PathBuf,
        downloads: PathBuf,
        frame_helper: Option<PathBuf>,
    ) -> Self {
        Self {
            executable,
            data_dir,
            downloads,
            frame_helper,
            process: Mutex::new(None),
        }
    }

    fn start(&self) -> Result<Process, Failure> {
        if !self.executable.is_file() {
            return Err(Failure::new("engine_unavailable"));
        }
        let mut command = Command::new(&self.executable);
        command
            .arg("--data-dir")
            .arg(&self.data_dir)
            .arg("--download-dir")
            .arg(&self.downloads)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .kill_on_drop(true);
        if let Some(helper) = self.frame_helper.as_ref().filter(|path| path.is_file()) {
            command.arg("--frame-helper").arg(helper);
        }
        #[cfg(target_os = "windows")]
        command.creation_flags(0x08000000); // CREATE_NO_WINDOW
        let mut child = command
            .spawn()
            .map_err(|_| Failure::new("engine_unavailable"))?;
        let input = child
            .stdin
            .take()
            .ok_or_else(|| Failure::new("engine_failed"))?;
        let output = child
            .stdout
            .take()
            .ok_or_else(|| Failure::new("engine_failed"))?;
        Ok(Process {
            child,
            input,
            output: BufReader::new(output),
            next_id: 1,
        })
    }

    pub async fn request(&self, method: &str, params: Option<Value>) -> Result<Value, Failure> {
        let mut process = self.process.lock().await;
        if process.is_none() {
            *process = Some(self.start()?);
        }
        let result = timeout(
            REQUEST_DEADLINE,
            process
                .as_mut()
                .expect("process started")
                .exchange(method, params),
        )
        .await;
        match result {
            Ok(Ok(reply)) => match reply.error {
                Some(failure) => Err(failure),
                None => Ok(reply.result.unwrap_or(Value::Null)),
            },
            _ => {
                // Keep the protocol synchronized after any partial write, read, or timeout.
                if let Some(process) = process.take() {
                    process.terminate().await;
                }
                Err(Failure::new("engine_failed"))
            }
        }
    }

    pub async fn shutdown(&self) {
        if let Some(process) = self.process.lock().await.take() {
            process.terminate().await;
        }
    }
}

impl Process {
    async fn exchange(&mut self, method: &str, params: Option<Value>) -> Result<Reply, ()> {
        let id = self.next_id;
        self.next_id += 1;
        let request =
            json!({ "id": id, "method": method, "params": params.unwrap_or_else(|| json!({})) });
        let mut request = serde_json::to_vec(&request).map_err(|_| ())?;
        request.push(b'\n');
        self.input.write_all(&request).await.map_err(|_| ())?;
        self.input.flush().await.map_err(|_| ())?;

        let mut bytes = Vec::new();
        (&mut self.output)
            .take(MAX_REPLY)
            .read_until(b'\n', &mut bytes)
            .await
            .map_err(|_| ())?;
        if bytes.last() != Some(&b'\n') {
            return Err(());
        }
        let response: Reply = serde_json::from_slice(&bytes).map_err(|_| ())?;
        if response.id != id {
            return Err(());
        }
        Ok(response)
    }

    async fn terminate(self) {
        let Process {
            mut child,
            input,
            output,
            ..
        } = self;
        drop(input);
        drop(output);
        if timeout(Duration::from_secs(3), child.wait()).await.is_err() {
            let _ = child.kill().await;
        }
    }
}
