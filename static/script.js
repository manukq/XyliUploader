const dropZone = document.getElementById('dropZone');
const fileInput = document.getElementById('fileInput');
const uploadBtn = document.getElementById('uploadBtn');
const historyBody = document.getElementById('historyBody');
const toast = document.getElementById('toast');

let selectedFiles = [];

function getCSRFToken() {
    const match = document.cookie.match(/(?:^|; )csrf_token=([^;]+)/);
    return match ? decodeURIComponent(match[1]) : '';
}

function csrfHeaders() {
    const headers = {};
    const token = getCSRFToken();
    if (token) {
        headers['X-CSRF-Token'] = token;
    }
    return headers;
}

async function checkAuth() {
    try {
        const response = await fetch('/auth/me');
        if (response.ok) {
            const data = await response.json();
            const loginBtn = document.getElementById('loginBtn');
            const userInfo = document.getElementById('userInfo');
            if (loginBtn) loginBtn.style.display = 'none';
            if (userInfo) {
                userInfo.style.display = 'flex';
                document.getElementById('username').textContent = data.username;
            }
        } else {
            const loginBtn = document.getElementById('loginBtn');
            const userInfo = document.getElementById('userInfo');
            if (loginBtn) loginBtn.style.display = 'flex';
            if (userInfo) userInfo.style.display = 'none';
        }
    } catch (error) {
        console.error('Auth check failed:', error);
    }
}

dropZone.addEventListener('click', () => fileInput.click());

dropZone.addEventListener('dragover', (e) => {
    e.preventDefault();
    dropZone.classList.add('dragover');
});

dropZone.addEventListener('dragleave', () => {
    dropZone.classList.remove('dragover');
});

dropZone.addEventListener('drop', (e) => {
    e.preventDefault();
    dropZone.classList.remove('dragover');

    const files = Array.from(e.dataTransfer.files);
    if (files.length > 0) {
        selectedFiles = selectedFiles.concat(files);
        updateDropZoneText();
    }
});

document.addEventListener('paste', (e) => {
    const dt = e.clipboardData;
    if (!dt) return;

    let file = null;

    if (dt.items && dt.items.length) {
        for (const item of dt.items) {
            if (item.kind === 'file' && item.type?.startsWith('image/')) {
                file = item.getAsFile();
                break;
            }
        }
    }

    if (!file && dt.files && dt.files.length) {
        for (const f of dt.files) {
            if (f.type?.startsWith('image/')) {
                file = f;
                break;
            }
        }
    }

    if (!file) return;

    e.preventDefault();

    const ext = (file.type.split('/')[1] || 'png').toLowerCase();
    const safeName = `pasted-${Date.now()}.${ext}`;
    selectedFiles = [new File([file], safeName, { type: file.type })];

    updateDropZoneText();

    if (!uploadBtn.disabled) uploadBtn.click();
});

fileInput.addEventListener('change', (e) => {
    if (e.target.files.length > 0) {
        selectedFiles = selectedFiles.concat(Array.from(e.target.files));
        updateDropZoneText();
    }
});

function updateDropZoneText() {
    const dropText = dropZone.querySelector('.drop-text');
    if (selectedFiles.length === 1) {
        dropText.textContent = selectedFiles[0].name;
    } else if (selectedFiles.length > 1) {
        dropText.textContent = `${selectedFiles.length} файлов выбрано`;
    }
}

uploadBtn.addEventListener('click', async () => {
    if (selectedFiles.length === 0) {
        showToast('Выберите файл');
        return;
    }

    const maxSize = 300 * 1024 * 1024;
    for (const file of selectedFiles) {
        if (file.size > maxSize) {
            showToast('Файл слишком большой');
            return;
        }
    }

    uploadBtn.disabled = true;
    uploadBtn.textContent = 'Загрузка...';

    const formData = new FormData();
    for (const file of selectedFiles) {
        formData.append('file', file);
    }

    try {
        const response = await fetch('/upload', {
            method: 'POST',
            headers: csrfHeaders(),
            body: formData
        });

        if (response.ok) {
            const data = await response.json();
            for (const file of data.files) {
                if (!file.error) {
                    saveToHistory(file.filename, file.link, file.deletion_link);
                }
            }
            loadHistory();

            selectedFiles = [];
            fileInput.value = '';
            const dropText = dropZone.querySelector('.drop-text');
            dropText.textContent = 'Перетащите или выберите файл';

            const successCount = data.files.filter(f => !f.error).length;
            const errorCount = data.files.filter(f => f.error).length;

            if (data.album_link) {
                copyToClipboard(data.album_link);
                showToast(`Альбом создан, ссылка скопирована`);
            } else if (errorCount > 0) {
                showToast(`Загружено ${successCount}, ошибок: ${errorCount}`);
            } else {
                showToast(`Загружено ${successCount} файлов`);
            }
        } else {
            showToast('Ошибка загрузки');
        }
    } catch (error) {
        showToast('Ошибка: ' + error.message);
    } finally {
        uploadBtn.disabled = false;
        uploadBtn.textContent = 'Upload';
    }
});

function saveToHistory(filename, url, deletionUrl) {
    let history = JSON.parse(localStorage.getItem('uploadHistory') || '[]');
    history.unshift({
        filename,
        url,
        deletionUrl,
        date: new Date().toISOString()
    });

    if (history.length > 50) {
        history = history.slice(0, 50);
    }

    localStorage.setItem('uploadHistory', JSON.stringify(history));
}

async function createAlbum() {
    const history = JSON.parse(localStorage.getItem('uploadHistory') || '[]');
    const fileIDs = history.map(item => {
        const match = item.url.match(/\/([^\/]+)$/);
        return match ? match[1] : null;
    }).filter(Boolean);

    if (fileIDs.length === 0) {
        showToast('Нет файлов для альбома');
        return;
    }

    try {
        const response = await fetch('/album/create', {
            method: 'POST',
            headers: {
                ...csrfHeaders(),
                'Content-Type': 'application/json'
            },
            body: JSON.stringify({ file_ids: fileIDs })
        });

        if (response.ok) {
            const data = await response.json();
            copyToClipboard(data.link);
            showToast('Альбом создана, ссылка скопирована');
        } else {
            showToast('Ошибка создания альбома');
        }
    } catch (error) {
        showToast('Ошибка: ' + error.message);
    }
}

function loadHistory() {
    const history = JSON.parse(localStorage.getItem('uploadHistory') || '[]');

    if (history.length === 0) {
        historyBody.innerHTML =
            '<tr><td colspan="4" style="text-align: center; color: #555; padding: 40px;">Нет загруженных файлов</td></tr>';
        return;
    }

    historyBody.innerHTML = history.map((item, index) => {
        const date = new Date(item.date);
        const formattedDate = date.toLocaleString('ru-RU', {
            day: '2-digit',
            month: '2-digit',
            year: 'numeric',
            hour: '2-digit',
            minute: '2-digit'
        });

        return `
            <tr>
                <td class="file-name">${item.filename}</td>
                <td class="file-date">${formattedDate}</td>
                <td><a href="${item.url}" class="file-link" target="_blank">${item.url}</a></td>
                <td>
                    <div class="actions-cell">
                        <button class="copy-btn-table" onclick="copyToClipboard('${item.url}')" title="Копировать">
                            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                                <rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect>
                                <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path>
                            </svg>
                        </button>
                        <button class="delete-btn-table" onclick="deleteFile(${index}, '${item.deletionUrl}')" title="Удалить">
                            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                                <polyline points="3 6 5 6 21 6"></polyline>
                                <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path>
                            </svg>
                        </button>
                    </div>
                </td>
            </tr>
        `;
    }).join('');
}

async function deleteFile(index, deletionUrl) {
    if (!confirm('Удалить файл?')) return;

    try {
        const response = await fetch(deletionUrl, {
            method: 'POST',
            headers: csrfHeaders()
        });

        if (response.ok) {
            let history = JSON.parse(localStorage.getItem('uploadHistory') || '[]');
            history.splice(index, 1);
            localStorage.setItem('uploadHistory', JSON.stringify(history));
            loadHistory();
            showToast('Файл удалён');
        } else {
            showToast('Ошибка удаления');
        }
    } catch (error) {
        showToast('Ошибка: ' + error.message);
    }
}

function copyAllLinks() {
    const history = JSON.parse(localStorage.getItem('uploadHistory') || '[]');
    if (history.length === 0) {
        showToast('Нет ссылок');
        return;
    }

    const links = history.map(item => item.url).join('\n');
    copyToClipboard(links);
    showToast(`Скопировано ${history.length} ссылок`);
}

async function deleteAllFiles() {
    const history = JSON.parse(localStorage.getItem('uploadHistory') || '[]');
    if (history.length === 0) {
        showToast('Нет файлов');
        return;
    }

    if (!confirm(`Удалить все файлы (${history.length})?`)) return;

    let deleted = 0;
    let errors = 0;

    for (let i = history.length - 1; i >= 0; i--) {
        try {
            const response = await fetch(history[i].deletionUrl, {
                method: 'POST',
                headers: csrfHeaders()
            });

            if (response.ok) {
                deleted++;
            } else {
                errors++;
            }
        } catch (error) {
            errors++;
        }
    }

    localStorage.removeItem('uploadHistory');
    loadHistory();

    if (errors > 0) {
        showToast(`Удалено ${deleted}, ошибок: ${errors}`);
    } else {
        showToast(`Удалено ${deleted} файлов`);
    }
}

function copyToClipboard(text) {
    navigator.clipboard.writeText(text).then(() => showToast('Скопировано'));
}

function showToast(message) {
    const toastSpan = toast.querySelector('span');
    toastSpan.textContent = message;
    toast.classList.add('show');

    setTimeout(() => toast.classList.remove('show'), 2000);
}

loadHistory();
checkAuth();
