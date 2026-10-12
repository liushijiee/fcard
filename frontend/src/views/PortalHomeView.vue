<script setup lang="ts">
import { userSession } from '../lib/user-session'
import { dateTime } from '../lib/format'
</script>

<template>
  <section v-if="userSession" class="panel"><div class="panel-heading"><div><h2>你好，{{ userSession.user.display_name }}</h2><p>这里只展示你的账号资料。</p></div><span class="status-pill active">账号正常</span></div><dl class="detail-list portal-details"><div><dt>显示名称</dt><dd>{{ userSession.user.display_name }}</dd></div><div><dt>登录邮箱</dt><dd>{{ userSession.user.email }}</dd></div><div><dt>用户编号</dt><dd class="mono">{{ userSession.user.id }}</dd></div><div><dt>创建时间</dt><dd>{{ dateTime(userSession.user.created_at) }}</dd></div><div><dt>密码状态</dt><dd>已完成密码设置</dd></div></dl></section><div class="info-note portal-note">沙盒卡片可在“我的卡片”中申请，需管理员启用并绑定已审核持卡人。登录成功不代表已通过 KYC 或发卡审核；入金和消费记账尚未接入。</div>
</template>
