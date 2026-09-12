<script setup lang="ts">
import { useRoute } from 'vue-router'
const route = useRoute()
</script>

<template>
  <a-card :bordered="false">
    <a-empty :description="(route.meta.title as string) + ' —— 建设中'" />
  </a-card>
</template>
