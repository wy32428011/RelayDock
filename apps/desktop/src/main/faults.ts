import type { Fault } from '@relaydock/protocol';

export class BridgeError extends Error {
  readonly fault: Fault;

  constructor(code: string, message: string, retryable = false) {
    super(message);
    this.fault = { code, message, retryable };
  }
}

// Core errors may be assembled from paths, network errors, or script output.
// Only relay stable codes, never the raw text or a child-process error stack.
const messages: Readonly<Record<string, string>> = {
  AUTH_REQUIRED: '缺少可用的连接凭据，请重新录入并测试连接。',
  AUTH_FAILED: '身份验证失败，请检查用户名、密码或私钥。',
  BAD_FRAME: '本地核心返回了无效的协议消息，请重新打开应用。',
  BAD_PARAMS: '参数格式无效，请检查后重试。',
  BUSY: '本地核心正在处理其他请求，请稍后重试。',
  CANCELLED: '请求已取消。',
  CATALOG_INVALID: '内置应用目录缺失或格式不正确，请检查客户端安装。',
  CONFIRM_REQUIRED: '请确认操作后再继续。',
  CONFIRMATION_REQUIRED: '请核对主机、来源和变更范围后确认操作。',
  CORE_TIMEOUT: '本地核心响应超时，结果未知。请检查任务状态，不要直接重试写操作。',
  CORE_UNAVAILABLE: '本地核心不可用，请重新打开应用。',
  CREDENTIAL_STORE_UNAVAILABLE: '系统凭据库不可用。请解锁后重试，或取消记住凭据并重新录入。',
  DIGEST_MISMATCH: '文件内容已变化，请重新查看并确认。',
  FRAME_LIMIT: '结果过大，请缩小查询范围。',
  HOST_KEY_CHANGED: '主机指纹已变化，请先核对服务器身份。',
  HOST_CHANGED: '主机连接信息已变更，请核对目标并重新提交。',
  HOST_KEY_REQUIRED: '请重新测试连接并核对主机指纹。',
  HOST_NOT_FOUND: '找不到该主机，请刷新主机列表。',
  HOST_UNTRUSTED: '请先测试连接并核对主机指纹。',
  INSTALL_UNSUPPORTED: '当前提供 Ubuntu 24.04 上 Mosquitto 2.1.2 的离线安装。其他远端组合可执行状态检测。',
  KEY_PASSPHRASE_INVALID: '无法解锁私钥，请检查口令。',
  KEY_PASSPHRASE_REQUIRED: '此私钥已加密，请输入私钥口令。',
  KEY_UNAVAILABLE: '无法读取选定的私钥，请检查格式、权限或重新选择。',
  ONLINE_UNSUPPORTED: '当前应用目录仅提供离线安装。',
  PLATFORM_UNKNOWN: '无法识别远端系统，请检查 SSH 命令权限并重新测试连接。',
  PROBE_EXPIRED: '连接测试已过期，请重新测试后保存。',
  RESOURCE_CHANGED: '选定的资源已变化，请重新检查后确认。',
  RESOURCE_CONFIRM_REQUIRED: '请先检查选定的资源，再确认导入。',
  RESOURCE_MISMATCH: '离线资源与选择的应用版本、目标系统或架构不匹配。',
  RESOURCE_REQUIRED: '请先导入匹配的离线资源。',
  RESOURCE_LIMIT: '离线资源缓存已达到上限，请检查设置和缓存用量。',
  RESOURCE_UNTRUSTED: '主包或依赖的摘要不在已核对清单内，请选用匹配的离线资源。',
  SCRIPT_UNAVAILABLE: '未找到适用于选定系统与操作的内置脚本。',
  SFTP_UNAVAILABLE: '主机不支持 SFTP，仍可使用已授权的 SSH 功能。',
  TIMEOUT: '操作超时，请检查连接或确认任务状态后再继续。',
  UNKNOWN_METHOD: '当前版本不支持此操作。',
  UNINSTALL_UNSUPPORTED: '当前提供本版安装的 Ubuntu 24.04 Mosquitto 2.1.2 保留数据卸载。',
  UNSUPPORTED_PLATFORM: '尚不支持选定的远端系统或架构。',
  VERSION_MISMATCH: '桌面应用与本地核心不兼容，请使用同一版本的安装包。',
  VERSION_UNSUPPORTED: '应用目录不包含选定的版本。',
};

function safeMessage(code: string): string {
  if (Object.hasOwn(messages, code)) return messages[code];
  if (code.startsWith('RESOURCE_')) return '无法使用选定的离线资源，请检查格式、完整性和适用平台。';
  if (code.startsWith('MIGRATION_')) return '无法完成迁移，请检查快照一致性、访问权限和预览结果。';
  if (code.startsWith('SSH_')) return 'SSH 操作未完成，请检查地址、凭据和连接状态。';
  if (code.startsWith('FILE_')) return '文件操作未完成，请检查所选文件、目标路径和权限。';
  return '操作未完成，请检查输入、连接和访问权限。';
}

export class DesktopFault extends BridgeError {
  constructor(code: string, retryable = false) {
    super(code, safeMessage(code), retryable);
  }
}

export function coreFault(fault: Fault): DesktopFault {
  return new DesktopFault(fault.code, fault.retryable);
}

export function publicFault(error: unknown): Fault {
  return error instanceof BridgeError ? error.fault : {
    code: 'INTERNAL', message: '桌面操作未完成，请检查权限或重试。', retryable: false,
  };
}
