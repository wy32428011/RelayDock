import { app } from 'electron';
import { runDesktop } from './desktop';

// Apply the workspace language to Chromium's built-in UI, including form validation.
app.commandLine.appendSwitch('lang', 'zh-CN');
runDesktop();
