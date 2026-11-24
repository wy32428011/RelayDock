import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import App from './App';
import './base.css';

const root = document.getElementById('root');
if (!root) throw new Error('找不到工作台的页面容器。');

createRoot(root).render(<StrictMode><App /></StrictMode>);
